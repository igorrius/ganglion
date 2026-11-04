// Package core runs turns: workers lease events from the inbox, hand each
// to a handler and store the handler's replies, the new conversation state
// and the "processed" mark in one transaction (docs/spec.md, 7.1, R-DLV-3
// and R-DLV-4). The queue leases one event per conversation at a time, so
// turns of one conversation never overlap while turns of different
// conversations run in parallel (R-AGT-1).
package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Handler handles one event: the replies to send and the conversation
// state to store. An error leaves the event to be handled again once its
// lease runs out.
type Handler func(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error)

// Defaults.
const (
	DefaultLease       = 10 * time.Minute
	DefaultPoll        = 30 * time.Second
	DefaultWorkers     = 4
	DefaultTurnTimeout = 3 * time.Minute
	DefaultMaxAttempts = 3
	// DefaultMaxInterrupted bounds a crash loop: each restart reruns the turn
	// from its start and pays for its model calls again.
	DefaultMaxInterrupted = 10
)

// Loop is the turn loop.
type Loop struct {
	q              queue.Queue
	ready          <-chan struct{}
	h              Handler
	log            *slog.Logger
	lease          time.Duration
	poll           time.Duration
	workers        int
	turnTimeout    time.Duration
	maxAttempts    int
	maxInterrupted int
	giveUp         GiveUp
	hook           TurnHook

	wake chan struct{} // a worker that got an event wakes an idle one

	tracer  trace.Tracer
	givenUp metric.Int64Counter
	reruns  metric.Int64Counter
}

// Option configures a Loop.
type Option func(*Loop)

// WithLease sets how long a turn may hold its event before the event is
// offered again; the default is DefaultLease.
func WithLease(d time.Duration) Option { return func(l *Loop) { l.lease = d } }

// WithPoll sets the safety re-check interval when nothing signals ready;
// the default is DefaultPoll.
func WithPoll(d time.Duration) Option { return func(l *Loop) { l.poll = d } }

// WithWorkers sets how many turns run at once; the default is
// DefaultWorkers.
func WithWorkers(n int) Option { return func(l *Loop) { l.workers = max(n, 1) } }

// WithTurnTimeout bounds the handler's context; keep it below the lease.
// The default is DefaultTurnTimeout.
func WithTurnTimeout(d time.Duration) Option { return func(l *Loop) { l.turnTimeout = d } }

// GiveUp returns the replies for an event that failed every one of its
// attempts; the event is completed with them instead of being retried.
type GiveUp func(e event.Event, attempts int) []event.Outgoing

// NoGiveUp gives up without replies.
func NoGiveUp(event.Event, int) []event.Outgoing { return nil }

// WithMaxInterrupted stops running an event that more than n restarts cut
// short, so an event that kills the process cannot do so forever (ADR-0046).
// The default is DefaultMaxInterrupted.
func WithMaxInterrupted(n int) Option {
	return func(l *Loop) { l.maxInterrupted = n }
}

// WithMaxAttempts stops retrying an event that failed more than n times: it is
// completed with giveUp's replies instead of reaching the handler again.
// The default is DefaultMaxAttempts with NoGiveUp.
func WithMaxAttempts(n int, giveUp GiveUp) Option {
	return func(l *Loop) { l.maxAttempts, l.giveUp = n, giveUp }
}

// TurnHook runs when a turn starts; the stop it returns runs when the
// turn ends. The node uses it to show "typing…" (ADR-0025).
type TurnHook func(ctx context.Context, e event.Event) (stop func())

// NoTurnHook does nothing.
func NoTurnHook(context.Context, event.Event) func() { return func() {} }

// WithTurnHook sets the turn hook; the default is NoTurnHook.
func WithTurnHook(h TurnHook) Option {
	return func(l *Loop) { l.hook = h }
}

// NewLoop returns a loop over q. ready is signalled when an event is put
// (sqlqueue.Queue.InboxReady).
func NewLoop(q queue.Queue, ready <-chan struct{}, h Handler, logger *slog.Logger, opts ...Option) *Loop {
	l := &Loop{q: q, ready: ready, h: h, log: logger, lease: DefaultLease, poll: DefaultPoll,
		workers: DefaultWorkers, turnTimeout: DefaultTurnTimeout, maxAttempts: DefaultMaxAttempts, maxInterrupted: DefaultMaxInterrupted, hook: NoTurnHook, giveUp: NoGiveUp}
	for _, o := range opts {
		o(l)
	}
	l.wake = make(chan struct{}, l.workers)
	l.tracer = otel.Tracer("ganglion/core")
	l.givenUp, _ = otel.Meter("ganglion/core").Int64Counter("ganglion.turns.given_up",
		metric.WithDescription("Events completed without a turn after failing every attempt."))
	l.reruns, _ = otel.Meter("ganglion/core").Int64Counter("ganglion.turns.interrupted",
		metric.WithDescription("Turns started over because a restart cut the last run short."))
	return l
}

// Run handles events with the configured workers until ctx ends, then
// returns nil. It never logs message text.
func (l *Loop) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for range l.workers {
		wg.Go(func() { l.worker(ctx) })
	}
	wg.Wait()
	return nil
}

func (l *Loop) worker(ctx context.Context) {
	for ctx.Err() == nil {
		lease, err := l.q.Lease(ctx, l.lease)
		switch {
		case errors.Is(err, queue.ErrEmpty):
			if !l.wait(ctx) {
				return
			}
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			l.log.Error("core: lease failed", "err", err)
			if !l.wait(ctx) {
				return
			}
			continue
		}
		// There may be more: let an idle worker look while this one works.
		if l.workers > 1 {
			select {
			case l.wake <- struct{}{}:
			default:
			}
		}
		l.turn(ctx, lease)
	}
}

// turn handles one leased event.
func (l *Loop) turn(ctx context.Context, lease queue.Lease) {
	e := lease.Event
	log := l.log.With("id", e.ID, "kind", e.Kind, "conv", e.Conv)
	ctx, span := l.tracer.Start(ctx, "turn", trace.WithAttributes(
		attribute.String("event.id", e.ID), attribute.String("event.kind", string(e.Kind)), attribute.Int("attempt", lease.Attempts)))
	defer span.End()
	if l.spent(lease) {
		l.givenUp.Add(ctx, 1)
		span.SetAttributes(attribute.String("outcome", "given_up"))
		runs := lease.Attempts - 1 + lease.Interrupted
		out := l.giveUp(e, runs)
		log.Error("core: giving up on an event that failed every attempt", "attempts", lease.Attempts-1, "interrupted", lease.Interrupted)
		l.complete(ctx, log, lease, out, queue.StateUpdate{})
		return
	}
	if lease.Interrupted > 0 {
		l.reruns.Add(ctx, 1)
		log.Info("core: starting a turn over after a restart", "interrupted", lease.Interrupted)
	}
	turnCtx, cancel := context.WithTimeout(ctx, l.turnTimeout)
	stop := l.hook(turnCtx, e)
	out, st, err := l.h(turnCtx, e)
	stop()
	cancel()
	if err != nil {
		span.SetStatus(codes.Error, "handler failed")
		if ctx.Err() == nil {
			log.Error("core: handler failed; the event is retried when its lease runs out", "err", err, "until", lease.Until, "attempts", lease.Attempts)
		}
		return
	}
	l.complete(ctx, log, lease, out, st)
}

// spent reports whether the event has used up its failures or its restarts.
func (l *Loop) spent(lease queue.Lease) bool {
	return (l.maxAttempts > 0 && lease.Attempts > l.maxAttempts) ||
		(l.maxInterrupted > 0 && lease.Interrupted > l.maxInterrupted)
}

func (l *Loop) complete(ctx context.Context, log *slog.Logger, lease queue.Lease, out []event.Outgoing, st queue.StateUpdate) {
	switch err := l.q.Complete(ctx, lease, out, st); {
	case errors.Is(err, queue.ErrLeaseLost):
		log.Warn("core: lease lost; the turn's replies are dropped")
	case err != nil:
		if ctx.Err() == nil {
			log.Error("core: complete failed", "err", err)
		}
	default:
		log.Debug("core: turn done", "replies", len(out))
	}
}

// wait sleeps until ready or another worker signals, the poll interval
// passes or ctx ends; it reports whether to go on.
func (l *Loop) wait(ctx context.Context) bool {
	timer := time.NewTimer(l.poll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-l.ready:
	case <-l.wake:
	case <-timer.C:
	}
	return true
}
