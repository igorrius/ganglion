// Package outbox delivers the messages waiting in a node's outbox through
// their transports (docs/spec.md, 7.1, R-DLV-5): in order within a
// conversation, with retries and backoff, honouring the channel's
// retry_after, and resuming split messages after the part already sent.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/internal/buckets"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/transport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// DefaultPoll is how often the sender re-checks the outbox when nothing
// wakes it, as a safety net for a missed wake signal.
const DefaultPoll = 30 * time.Second

// batch is how many messages one pass takes from the outbox.
const batch = 100

// retryPolicy is the wait between failed sends of one message.
var retryPolicy = backoff.Policy{Min: time.Second, Max: 5 * time.Minute}

// DefaultBackoff returns the wait before retry number attempt (from 1):
// 1 s·2^(attempt-1) with ±20% jitter, capped at 5 minutes.
func DefaultBackoff(attempt int) time.Duration { return retryPolicy.Delay(attempt) }

// Sender delivers the outbox. It is one goroutine and sends sequentially,
// which keeps the order within a conversation trivially correct.
type Sender struct {
	ob         queue.Outbox
	wake       <-chan struct{}
	log        *slog.Logger
	now        func() time.Time
	after      func(time.Duration) <-chan time.Time
	backoff    func(attempt int) time.Duration
	poll       time.Duration
	transports map[string]transport.Transport
	paused     map[string]time.Time // transport name → end of its retry_after pause
	missing    map[string]bool      // unregistered transports already logged

	tracer  trace.Tracer
	sent    metric.Int64Counter
	retries metric.Int64Counter
	failedN metric.Int64Counter
	attachN metric.Int64Counter   // attachments by kind and result
	attachB metric.Int64Histogram // bytes of attachments delivered, by kind
}

// Option configures a Sender.
type Option func(*Sender)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(s *Sender) { s.now = now } }

// WithBackoff sets the wait before retry number attempt; the default is
// DefaultBackoff.
func WithBackoff(f func(attempt int) time.Duration) Option {
	return func(s *Sender) { s.backoff = f }
}

// WithPoll sets the safety re-check interval; the default is DefaultPoll.
func WithPoll(d time.Duration) Option { return func(s *Sender) { s.poll = d } }

// withAfter replaces time.After, for tests that run on virtual time.
func withAfter(f func(time.Duration) <-chan time.Time) Option {
	return func(s *Sender) { s.after = f }
}

// NewSender returns a sender for ob. wake is signalled when new messages
// are stored (sqlqueue.Queue.OutboxReady).
func NewSender(ob queue.Outbox, wake <-chan struct{}, logger *slog.Logger, opts ...Option) *Sender {
	s := &Sender{
		ob:         ob,
		wake:       wake,
		log:        logger,
		now:        time.Now,
		after:      time.After,
		backoff:    DefaultBackoff,
		poll:       DefaultPoll,
		transports: map[string]transport.Transport{},
		paused:     map[string]time.Time{},
		missing:    map[string]bool{},
	}
	for _, o := range opts {
		o(s)
	}
	m := otel.Meter("ganglion/outbox")
	s.tracer = otel.Tracer("ganglion/outbox")
	s.sent, _ = m.Int64Counter("ganglion.outbox.sent", metric.WithDescription("Messages delivered, by transport."))
	s.retries, _ = m.Int64Counter("ganglion.outbox.retries", metric.WithDescription("Failed sends to retry, by transport and kind."))
	s.failedN, _ = m.Int64Counter("ganglion.outbox.failed", metric.WithDescription("Messages given up for good, by transport."))
	s.attachN, _ = m.Int64Counter("ganglion.outbox.attachments", metric.WithDescription("Attachments delivered or given up, by kind and result."))
	s.attachB, _ = m.Int64Histogram("ganglion.outbox.attachment.bytes", metric.WithUnit("By"), buckets.Bytes,
		metric.WithDescription("Size of the attachments delivered, by kind."))
	return s
}

// Register adds a transport by its Name. A message for a transport that is
// not registered waits in the outbox; it is not failed. Register before Run.
func (s *Sender) Register(t transport.Transport) { s.transports[t.Name()] = t }

// Run delivers the outbox until ctx ends. Outbox errors are logged and
// retried at the poll interval; Run returns nil when ctx ends.
func (s *Sender) Run(ctx context.Context) error {
	for {
		progress, next, err := s.pass(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			s.log.Error("outbox: pass failed", "err", err)
		}
		if progress && err == nil {
			continue // something moved: there may be more right away
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
		case <-s.after(s.waitFor(next)):
		}
	}
}

// waitFor returns how long to sleep: until the earliest of next, the end
// of a transport pause and the poll interval.
func (s *Sender) waitFor(next time.Time) time.Duration {
	now := s.now()
	d := s.poll
	consider := func(t time.Time) {
		if !t.IsZero() && t.Sub(now) < d {
			d = max(t.Sub(now), 0)
		}
	}
	consider(next)
	for name, until := range s.paused {
		if !until.After(now) {
			delete(s.paused, name)
			continue
		}
		consider(until)
	}
	return d
}

// pass handles the messages due now. It reports whether any message
// changed state, and the outbox's next retry time.
func (s *Sender) pass(ctx context.Context) (progress bool, next time.Time, err error) {
	due, next, err := s.ob.Due(ctx, batch)
	if err != nil {
		return false, time.Time{}, err
	}
	for _, p := range due {
		if ctx.Err() != nil {
			return progress, next, nil
		}
		handled, err := s.deliver(ctx, p)
		if err != nil {
			return progress, next, err
		}
		progress = progress || handled
	}
	return progress, next, nil
}

// deliver sends one message and records the outcome. It reports whether
// the message changed state; an error is an outbox error, not a send error.
func (s *Sender) deliver(ctx context.Context, p queue.Pending) (bool, error) {
	msg := p.Msg
	log := s.log.With("id", msg.ID, "transport", msg.Transport, "conv", msg.Conv, "attempts", p.Attempts)
	t, ok := s.transports[msg.Transport]
	if !ok {
		// The transport may be off for now (no token after a config slip):
		// keep the message for when it is back, never fail it.
		if !s.missing[msg.Transport] {
			s.missing[msg.Transport] = true
			log.Warn("outbox: transport not registered; its messages wait")
		}
		return false, nil
	}
	if until, ok := s.paused[msg.Transport]; ok && s.now().Before(until) {
		return false, nil
	}

	parts := []event.Outgoing{msg}
	if pt, ok := t.(transport.Parter); ok {
		parts = pt.Parts(msg)
	}
	ctx, span := s.tracer.Start(ctx, "outbox.send", trace.WithAttributes(
		attribute.String("message.id", msg.ID), attribute.String("transport", msg.Transport), attribute.Int("parts", len(parts))))
	defer span.End()
	ref := "" // of the last part with one, which holds a message's buttons
	for i := p.PartsSent; i < len(parts); i++ {
		r, err := t.Send(ctx, parts[i])
		if err != nil {
			if ctx.Err() != nil {
				return false, nil // shutting down: the message stays as it is
			}
			return s.failed(ctx, log, p, err)
		}
		if r != "" {
			ref = r
		}
		if len(parts) > 1 {
			if err := s.ob.PartSent(ctx, msg.ID, i+1); err != nil {
				return true, err
			}
		}
	}
	if err := s.ob.Sent(ctx, msg.ID, ref); err != nil {
		return true, err
	}
	s.countSent(ctx, msg)
	log.Info("outbox: sent", "parts", len(parts), "len", len(msg.Text))
	return true, nil
}

// countSent counts a delivered message and its attachment.
func (s *Sender) countSent(ctx context.Context, msg event.Outgoing) {
	s.sent.Add(ctx, 1, metric.WithAttributes(attribute.String("transport", msg.Transport)))
	if a := msg.Attach; a != nil {
		kind := attribute.String("kind", string(a.Kind))
		s.attachN.Add(ctx, 1, metric.WithAttributes(kind, attribute.String("result", "sent")))
		s.attachB.Record(ctx, int64(len(a.Data)), metric.WithAttributes(kind))
	}
}

// failed records a failed send according to the kind of error.
func (s *Sender) failed(ctx context.Context, log *slog.Logger, p queue.Pending, sendErr error) (bool, error) {
	var ra *transport.RetryAfterError
	tr := attribute.String("transport", p.Msg.Transport)
	trace.SpanFromContext(ctx).SetStatus(codes.Error, "send failed")
	switch {
	case errors.As(sendErr, &ra):
		s.retries.Add(ctx, 1, metric.WithAttributes(tr, attribute.String("kind", "retry_after")))
		// Pause the whole transport; the message stays due and is not
		// charged an attempt. The pause lives in memory: after a restart
		// the channel says retry_after again if it still means it.
		until := s.now().Add(ra.After)
		s.paused[p.Msg.Transport] = until
		log.Warn("outbox: transport asked to wait", "until", until)
		return false, nil
	case errors.Is(sendErr, transport.ErrPermanent):
		s.failedN.Add(ctx, 1, metric.WithAttributes(tr))
		if a := p.Msg.Attach; a != nil {
			s.attachN.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(a.Kind)), attribute.String("result", "failed")))
		}
		log.Error("outbox: permanent failure, giving up", "err", sendErr)
		return true, s.ob.Fail(ctx, p.Msg.ID, sendErr.Error())
	default:
		s.retries.Add(ctx, 1, metric.WithAttributes(tr, attribute.String("kind", "error")))
		notBefore := s.now().Add(s.backoff(p.Attempts + 1))
		log.Warn("outbox: send failed, will retry", "err", sendErr, "not_before", notBefore)
		return true, s.ob.Retry(ctx, p.Msg.ID, notBefore, sendErr.Error())
	}
}
