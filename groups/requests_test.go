package groups_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

const admin = "telegram:user:42"

// inbox records the events put, and fails when told to.
type inbox struct {
	events []event.Event
	fail   error
}

func (i *inbox) put(_ context.Context, e event.Event) error {
	if i.fail != nil {
		return i.fail
	}
	i.events = append(i.events, e)
	return nil
}

func admins(list ...string) alert.Admins {
	return alert.Admins{
		List:    func(context.Context) ([]string, error) { return list, nil },
		Private: func(p string) (string, string, bool) { return "telegram", "telegram:42", p == admin },
	}
}

func newRequests(t *testing.T, s *groups.Store, in *inbox, a alert.Admins) groups.Requests {
	t.Helper()
	return groups.NewRequests(s, a, in.put, func() time.Time { return t0 }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func seenIn(group string) event.GroupSeen {
	return event.GroupSeen{Group: group, Conv: group + "/45", Title: "Family", From: "telegram:user:9", Name: "Ann"}
}

func TestSeenAsksTheAdminOnce(t *testing.T) {
	ctx := context.Background()
	rec := telemetrytest.Install(t)
	s, in := newStore(t, &clock{at: t0}), &inbox{}
	r := newRequests(t, s, in, admins(admin))
	for range 2 {
		if err := r.Seen(ctx, seenIn(family)); err != nil {
			t.Fatal(err)
		}
	}
	if len(in.events) != 1 {
		t.Fatalf("%d events, want one request", len(in.events))
	}
	e := in.events[0]
	if e.Kind != event.KindGroupRequest || e.Source != event.SourceGroups || e.Conv != "telegram:42" || e.ID == "" || e.IdemKey == "" {
		t.Fatalf("event %+v", e)
	}
	var req event.GroupRequest
	if err := json.Unmarshal(e.Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.GroupSeen != seenIn(family) || req.Principal != admin || req.Transport != "telegram" {
		t.Fatalf("request %+v", req)
	}
	if st, ok := status(t, s, family); !ok || st != groups.StatusPending {
		t.Fatalf("status %q %v", st, ok)
	}
	for o, want := range map[string]float64{"asked": 1, "known": 1} {
		if got := rec.Value(t, "ganglion.groups.requests", map[string]string{"outcome": o}); got != want {
			t.Errorf("requests{outcome=%s} = %v, want %v", o, got, want)
		}
	}
}

func TestSeenStaysSilentAfterARefusal(t *testing.T) {
	ctx := context.Background()
	s, in := newStore(t, &clock{at: t0}), &inbox{}
	if err := s.Set(ctx, family, "Family", groups.StatusRefused); err != nil {
		t.Fatal(err)
	}
	if err := newRequests(t, s, in, admins(admin)).Seen(ctx, seenIn(family)); err != nil || len(in.events) != 0 {
		t.Fatalf("err %v, %d events after a refusal", err, len(in.events))
	}
}

// Abuse: a stranger who adds the bot to many groups cannot flood the admin.
func TestSeenCapsWaitingRequests(t *testing.T) {
	ctx := context.Background()
	rec := telemetrytest.Install(t)
	s, in := newStore(t, &clock{at: t0}), &inbox{}
	r := newRequests(t, s, in, admins(admin))
	for i := range groups.MaxPending + 5 {
		if err := r.Seen(ctx, seenIn("telegram:-"+strconv.Itoa(2000+i))); err != nil {
			t.Fatal(err)
		}
	}
	if len(in.events) != groups.MaxPending || waiting(t, s) != groups.MaxPending {
		t.Fatalf("%d events, %d pending, want %d of each", len(in.events), waiting(t, s), groups.MaxPending)
	}
	if got := rec.Value(t, "ganglion.groups.requests", map[string]string{"outcome": "capped"}); got != 5 {
		t.Errorf("requests{outcome=capped} = %v, want 5", got)
	}
}

func TestSeenWithNoAdminAsksNobody(t *testing.T) {
	ctx := context.Background()
	s, in := newStore(t, &clock{at: t0}), &inbox{}
	// One admin is listed but has no private chat; another list is empty.
	for _, a := range []alert.Admins{admins(), admins("matrix:user:x")} {
		if err := newRequests(t, s, in, a).Seen(ctx, seenIn(family)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := status(t, s, family); ok || len(in.events) != 0 {
		t.Fatalf("%d events and a stored request with nobody to ask", len(in.events))
	}
}

// A request that could not be put is not remembered: the next message asks again.
func TestSeenReturnsAPutError(t *testing.T) {
	ctx := context.Background()
	s, in := newStore(t, &clock{at: t0}), &inbox{fail: errors.New("inbox unavailable")}
	r := newRequests(t, s, in, admins(admin))
	if err := r.Seen(ctx, seenIn(family)); err == nil {
		t.Fatal("Seen = nil with a failing inbox")
	}
	if _, ok := status(t, s, family); ok {
		t.Fatal("a request that was not put is remembered")
	}
	in.fail = nil
	if err := r.Seen(ctx, seenIn(family)); err != nil || len(in.events) != 1 {
		t.Fatalf("err %v, %d events on the second try", err, len(in.events))
	}
}
