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
)

// Loop is the turn loop.
type Loop struct {
	q           queue.Queue
	ready       <-chan struct{}
	h           Handler
	log         *slog.Logger
	lease       time.Duration
	poll        time.Duration
	workers     int
	turnTimeout time.Duration
	maxAttempts int
	giveUp      func(e event.Event) []event.Outgoing

	wake chan struct{} // a worker that got an event wakes an idle one
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

// WithMaxAttempts stops retrying an event leased more than n times: it is
// completed with giveUp's replies (nil: none) instead of reaching the
// handler again. The default is DefaultMaxAttempts with no replies.
func WithMaxAttempts(n int, giveUp func(e event.Event) []event.Outgoing) Option {
	return func(l *Loop) { l.maxAttempts, l.giveUp = n, giveUp }
}

// NewLoop returns a loop over q. ready is signalled when an event is put
// (sqlqueue.Queue.InboxReady).
func NewLoop(q queue.Queue, ready <-chan struct{}, h Handler, logger *slog.Logger, opts ...Option) *Loop {
	l := &Loop{q: q, ready: ready, h: h, log: logger, lease: DefaultLease, poll: DefaultPoll,
		workers: DefaultWorkers, turnTimeout: DefaultTurnTimeout, maxAttempts: DefaultMaxAttempts}
	for _, o := range opts {
		o(l)
	}
	l.wake = make(chan struct{}, l.workers)
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
	if l.maxAttempts > 0 && lease.Attempts > l.maxAttempts {
		var out []event.Outgoing
		if l.giveUp != nil {
			out = l.giveUp(e)
		}
		log.Error("core: giving up on an event that failed every attempt", "attempts", lease.Attempts-1)
		l.complete(ctx, log, lease, out, queue.StateUpdate{})
		return
	}
	turnCtx, cancel := context.WithTimeout(ctx, l.turnTimeout)
	out, st, err := l.h(turnCtx, e)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Error("core: handler failed; the event is retried when its lease runs out", "err", err, "until", lease.Until, "attempts", lease.Attempts)
		}
		return
	}
	l.complete(ctx, log, lease, out, st)
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
