// Package core runs turns: it leases events from the inbox one at a time,
// hands each to a handler and stores the handler's replies, the new
// conversation state and the "processed" mark in one transaction
// (docs/spec.md, 7.1, R-DLV-3 and R-DLV-4).
package core

import (
	"context"
	"errors"
	"log/slog"
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
	DefaultLease = 10 * time.Minute
	DefaultPoll  = 30 * time.Second
)

// Loop is the turn loop.
type Loop struct {
	q     queue.Queue
	ready <-chan struct{}
	h     Handler
	log   *slog.Logger
	lease time.Duration
	poll  time.Duration
}

// Option configures a Loop.
type Option func(*Loop)

// WithLease sets how long a turn may take before the event is offered
// again; the default is DefaultLease.
func WithLease(d time.Duration) Option { return func(l *Loop) { l.lease = d } }

// WithPoll sets the safety re-check interval when nothing signals ready;
// the default is DefaultPoll.
func WithPoll(d time.Duration) Option { return func(l *Loop) { l.poll = d } }

// NewLoop returns a loop over q. ready is signalled when an event is put
// (sqlqueue.Queue.InboxReady).
func NewLoop(q queue.Queue, ready <-chan struct{}, h Handler, logger *slog.Logger, opts ...Option) *Loop {
	l := &Loop{q: q, ready: ready, h: h, log: logger, lease: DefaultLease, poll: DefaultPoll}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Run handles events until ctx ends, then returns nil. It never logs
// message text.
func (l *Loop) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		lease, err := l.q.Lease(ctx, l.lease)
		switch {
		case errors.Is(err, queue.ErrEmpty):
			if !l.wait(ctx) {
				return nil
			}
			continue
		case err != nil:
			if ctx.Err() != nil {
				return nil
			}
			l.log.Error("core: lease failed", "err", err)
			if !l.wait(ctx) {
				return nil
			}
			continue
		}
		l.turn(ctx, lease)
	}
}

// turn handles one leased event.
func (l *Loop) turn(ctx context.Context, lease queue.Lease) {
	e := lease.Event
	log := l.log.With("id", e.ID, "kind", e.Kind, "conv", e.Conv)
	out, st, err := l.h(ctx, e)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("core: handler failed; the event is retried when its lease runs out", "err", err, "until", lease.Until)
		}
		return
	}
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

// wait sleeps until ready is signalled, the poll interval passes or ctx
// ends; it reports whether to go on.
func (l *Loop) wait(ctx context.Context) bool {
	timer := time.NewTimer(l.poll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-l.ready:
	case <-timer.C:
	}
	return true
}
