package pending

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/event"
)

// ExpireEvery is how often the Expirer looks for expired calls.
const ExpireEvery = 30 * time.Second

// PutFunc stores an event in the inbox; an IdemKey already stored is a
// no-op (queue.Queue.Put).
type PutFunc func(ctx context.Context, e event.Event) error

// AfterFunc waits like time.After.
type AfterFunc func(d time.Duration) <-chan time.Time

// Expirer is a node component: every ExpireEvery it expires the calls
// nobody answered in time and puts a KindToolResult event for each into
// its conversation, so the turn resumes with "expired" (R-POL-2).
type Expirer struct {
	store *Store
	put   PutFunc
	log   *slog.Logger
	after AfterFunc
}

// ExpirerOption configures an Expirer.
type ExpirerOption func(*Expirer)

// WithAfter sets how the Expirer waits between passes; the default is
// time.After.
func WithAfter(f AfterFunc) ExpirerOption { return func(x *Expirer) { x.after = f } }

// NewExpirer returns an Expirer over s that puts its events with put.
func NewExpirer(s *Store, put PutFunc, log *slog.Logger, opts ...ExpirerOption) *Expirer {
	x := &Expirer{store: s, put: put, log: log, after: time.After}
	for _, o := range opts {
		o(x)
	}
	return x
}

// Name implements node.Component.
func (*Expirer) Name() string { return "expire-pending" }

// Run expires calls at start and then every ExpireEvery until ctx ends;
// it returns nil then. A failed pass is logged and tried again.
func (x *Expirer) Run(ctx context.Context) error {
	for {
		x.pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-x.after(ExpireEvery):
		}
	}
}

// pass puts an event for every expired call whose outcome has not reached
// its conversation. Its key is fixed per call, so a call offered again on
// the next pass is stored once.
func (x *Expirer) pass(ctx context.Context) {
	items, err := x.store.ExpireDue(ctx)
	if err != nil {
		if ctx.Err() == nil {
			x.log.Error("pending: expiry failed", "err", err)
		}
		return
	}
	for _, it := range items {
		if err := x.put(ctx, expiredEvent(it, x.store.now())); err != nil {
			if ctx.Err() == nil {
				x.log.Error("pending: expiry event not stored", "pending", it.ID, "conv", it.Conv, "err", err)
			}
			return
		}
	}
}

// expiredEvent is the event that resumes the conversation of an expired
// call. Its Source is the conversation's transport, so the answer goes
// back there.
func expiredEvent(it Item, now time.Time) event.Event {
	payload, err := json.Marshal(event.ToolResult{Pending: it.ID})
	if err != nil {
		panic("pending: a struct of strings always marshals: " + err.Error())
	}
	return event.Event{
		ID: event.NewID(now), IdemKey: "pending:" + it.ID + ":expired", Source: it.Transport, Conv: it.Conv,
		Kind: event.KindToolResult, Payload: payload, At: now,
	}
}
