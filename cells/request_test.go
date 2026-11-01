package cells_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

func ask(t *testing.T, s *cells.Service, ttl time.Duration) cells.Cell {
	t.Helper()
	c, err := s.Ask(context.Background(), cells.Request{Question: "Switch the big boiler to Tuya?"}, ttl, "agent")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decision(t *testing.T, c cells.Cell) cells.Decision {
	t.Helper()
	d, err := cells.DecisionOf(c)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAskCreatesPendingRequest(t *testing.T) {
	s, clk := newService(t)
	a := ask(t, s, time.Minute)
	b := ask(t, s, time.Minute)
	if a.ID == b.ID || !strings.HasPrefix(a.ID, "request.") || len(a.ID) != len("request.")+16 {
		t.Fatalf("ids %q and %q", a.ID, b.ID)
	}
	if a.Kind != cells.KindRequest || decision(t, a).Status != cells.StatusPending {
		t.Fatalf("request = %+v", a)
	}
	if !a.Expires.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("expires %v, want now + 1 min", a.Expires)
	}
	var r cells.Request
	if err := jsonUnmarshal(a.Desired, &r); err != nil || r.Question == "" {
		t.Fatalf("desired = %s", a.Desired)
	}
}

func TestAnswerApproves(t *testing.T) {
	s, _ := newService(t)
	a := ask(t, s, time.Minute)
	c, err := s.Answer(context.Background(), a.ID, true, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if d := decision(t, c); d.Status != cells.StatusApproved || d.By != "owner" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestAnswerTwiceIsRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a := ask(t, s, time.Minute)
	if _, err := s.Answer(ctx, a.ID, false, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(ctx, a.ID, true, "owner"); !errors.Is(err, cells.ErrDecided) {
		t.Fatalf("second answer = %v, want ErrDecided", err)
	}
	c, _ := s.Get(ctx, a.ID)
	if decision(t, c).Status != cells.StatusDenied {
		t.Fatalf("decision changed: %s", c.Reported)
	}
}

func TestAnswerAfterTTLExpires(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	a := ask(t, s, time.Minute)
	clk.Add(61 * time.Second)
	c, err := s.Answer(ctx, a.ID, true, "owner")
	if !errors.Is(err, cells.ErrDecided) {
		t.Fatalf("late answer = %v, want ErrDecided", err)
	}
	if decision(t, c).Status != cells.StatusExpired {
		t.Fatalf("late answer result = %s, want expired", c.Reported)
	}
	stored, _ := s.Get(ctx, a.ID)
	if decision(t, stored).Status != cells.StatusExpired {
		t.Fatalf("stored = %s, want expired", stored.Reported)
	}
}

func TestExpireDueMarksOnlyDue(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	short := ask(t, s, time.Minute)
	long := ask(t, s, time.Hour)
	clk.Add(2 * time.Minute)
	n, err := s.ExpireDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireDue = %d (%v), want 1", n, err)
	}
	a, _ := s.Get(ctx, short.ID)
	b, _ := s.Get(ctx, long.ID)
	if decision(t, a).Status != cells.StatusExpired || decision(t, b).Status != cells.StatusPending {
		t.Fatalf("short=%s long=%s", a.Reported, b.Reported)
	}
}

func TestRequestRejectsDesiredAndReport(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a := ask(t, s, time.Minute)
	if _, err := s.SetDesired(ctx, a.ID, on, "agent", 0); !errors.Is(err, cells.ErrWrongKind) {
		t.Fatalf("SetDesired on a request = %v", err)
	}
	if _, err := s.Report(ctx, a.ID, on, cells.CauseExternal, "x"); !errors.Is(err, cells.ErrWrongKind) {
		t.Fatalf("Report on a request = %v", err)
	}
}
