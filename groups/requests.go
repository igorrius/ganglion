package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
)

// PutFunc puts an event into the inbox.
type PutFunc func(ctx context.Context, e event.Event) error

// Requests turns a group that is not connected into one request to an
// admin, in the admin's private chat (R-DLV-17).
type Requests struct {
	store  *Store
	admins alert.Admins
	put    PutFunc
	now    func() time.Time
	log    *slog.Logger
	in     instruments
}

// NewRequests returns the requests over s.
func NewRequests(s *Store, admins alert.Admins, put PutFunc, now func() time.Time, log *slog.Logger) Requests {
	return Requests{store: s, admins: admins, put: put, now: now, log: log, in: newInstruments()}
}

// Seen is called for a group that is not connected when someone addresses
// the bot in it or adds the bot to it. The first call asks an admin; while
// that request waits, after a refusal, and past MaxPending waiting
// requests it does nothing. An error means nothing was asked: the caller
// offers the update again.
func (r Requests) Seen(ctx context.Context, g event.GroupSeen) error {
	o, err := r.seen(ctx, g)
	if err != nil {
		return err
	}
	r.in.count(ctx, o)
	r.log.Info("groups: a group that is not connected", "group", g.Group, "outcome", string(o))
	return nil
}

func (r Requests) seen(ctx context.Context, g event.GroupSeen) (outcome, error) {
	if _, known, err := r.store.Status(ctx, g.Group); err != nil || known {
		return outcomeKnown, err
	}
	n, err := r.store.Pending(ctx)
	if err != nil {
		return "", err
	}
	if n >= MaxPending {
		return outcomeCapped, nil
	}
	req, conv, ok, err := r.address(ctx, g)
	if err != nil || !ok {
		return outcomeNoAdmin, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("groups: %w", err)
	}
	now := r.now()
	// The key holds the day: a group whose request ran out or was forgotten
	// can ask again on another day, and a put repeated the same day is
	// stored once.
	e := event.Event{ID: event.NewID(now), IdemKey: "group:request:" + g.Group + ":" + now.UTC().Format(time.DateOnly),
		Source: event.SourceGroups, Conv: conv, Kind: event.KindGroupRequest, Payload: payload, At: now}
	if err := r.put(ctx, e); err != nil {
		return "", fmt.Errorf("groups: request for %s: %w", g.Group, err)
	}
	return outcomeAsked, r.store.Set(ctx, g.Group, g.Title, StatusPending)
}

// address picks the first admin with a private chat and writes the
// request for them.
func (r Requests) address(ctx context.Context, g event.GroupSeen) (event.GroupRequest, string, bool, error) {
	admins, err := r.admins.List(ctx)
	if err != nil {
		return event.GroupRequest{}, "", false, fmt.Errorf("groups: %w", err)
	}
	for _, p := range admins {
		if transport, conv, ok := r.admins.Private(p); ok {
			return event.GroupRequest{GroupSeen: g, Principal: p, Transport: transport}, conv, true, nil
		}
	}
	return event.GroupRequest{}, "", false, nil
}
