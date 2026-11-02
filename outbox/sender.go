// Package outbox delivers the messages waiting in a node's outbox through
// their transports (docs/spec.md, 7.1, R-DLV-5): in order within a
// conversation, with retries and backoff, honouring the channel's
// retry_after, and resuming split messages after the part already sent.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/transport"
)

// DefaultPoll is how often the sender re-checks the outbox when nothing
// wakes it, as a safety net for a missed wake signal.
const DefaultPoll = 30 * time.Second

// batch is how many messages one pass takes from the outbox.
const batch = 100

const (
	backoffBase = time.Second
	backoffCap  = 5 * time.Minute
)

// DefaultBackoff returns the wait before retry number attempt (from 1):
// 1 s·2^(attempt-1) with ±20% jitter, capped at 5 minutes.
func DefaultBackoff(attempt int) time.Duration {
	d := backoffCap
	if attempt < 1 {
		attempt = 1
	}
	if attempt <= 20 {
		d = backoffBase << (attempt - 1)
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	return min(d, backoffCap)
}

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
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Register adds a transport by its Name. A message for a transport that is
// not registered fails for good. Register before Run.
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
		log.Error("outbox: unknown transport, giving up")
		return true, s.ob.Fail(ctx, msg.ID, "unknown transport "+msg.Transport)
	}
	if until, ok := s.paused[msg.Transport]; ok && s.now().Before(until) {
		return false, nil
	}

	parts := []event.Outgoing{msg}
	if pt, ok := t.(transport.Parter); ok {
		parts = pt.Parts(msg)
	}
	for i := p.PartsSent; i < len(parts); i++ {
		if err := t.Send(ctx, parts[i]); err != nil {
			if ctx.Err() != nil {
				return false, nil // shutting down: the message stays as it is
			}
			return s.failed(ctx, log, p, err)
		}
		if len(parts) > 1 {
			if err := s.ob.PartSent(ctx, msg.ID, i+1); err != nil {
				return true, err
			}
		}
	}
	if err := s.ob.Sent(ctx, msg.ID); err != nil {
		return true, err
	}
	log.Debug("outbox: sent", "parts", len(parts), "len", len(msg.Text))
	return true, nil
}

// failed records a failed send according to the kind of error.
func (s *Sender) failed(ctx context.Context, log *slog.Logger, p queue.Pending, sendErr error) (bool, error) {
	var ra *transport.RetryAfterError
	switch {
	case errors.As(sendErr, &ra):
		// Pause the whole transport; the message stays due and is not
		// charged an attempt. The pause lives in memory: after a restart
		// the channel says retry_after again if it still means it.
		until := s.now().Add(ra.After)
		s.paused[p.Msg.Transport] = until
		log.Warn("outbox: transport asked to wait", "until", until)
		return false, nil
	case errors.Is(sendErr, transport.ErrPermanent):
		log.Error("outbox: permanent failure, giving up", "err", sendErr)
		return true, s.ob.Fail(ctx, p.Msg.ID, sendErr.Error())
	default:
		notBefore := s.now().Add(s.backoff(p.Attempts + 1))
		log.Warn("outbox: send failed, will retry", "err", sendErr, "not_before", notBefore)
		return true, s.ob.Retry(ctx, p.Msg.ID, notBefore, sendErr.Error())
	}
}
