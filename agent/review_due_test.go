package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
)

// puts records the events a timer puts.
type puts struct{ got []event.Event }

func (p *puts) put(_ context.Context, e event.Event) error {
	p.got = append(p.got, e)
	return nil
}

func reviewDue(t *testing.T) *ReviewDue {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	d, err := NewReviewDue(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A due row becomes one memory.review event and is removed; one not due
// stays; a row armed again later stays for its new time (R-MEM-8).
func TestReviewTimerPutsDueRows(t *testing.T) {
	ctx := context.Background()
	d := reviewDue(t)
	now := t0
	p := &puts{}
	timer := NewReviewTimer(d, p.put, func() time.Time { return now }, time.Second, discardLog())
	if err := d.Arm(ctx, "telegram:42", "", "telegram", owner, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Arm(ctx, "telegram:42", "s1", "telegram", owner, t0); err != nil {
		t.Fatal(err)
	}
	if err := timer.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.got) != 1 {
		t.Fatalf("events %+v", p.got)
	}
	e := p.got[0]
	var r event.MemoryReview
	if err := json.Unmarshal(e.Payload, &r); err != nil || r.Principal != owner || r.Session != "s1" {
		t.Fatalf("payload %s %v", e.Payload, err)
	}
	if e.Kind != event.KindMemoryReview || e.Conv != "telegram:42" || e.Source != "telegram" ||
		e.IdemKey != "memory.review:telegram:42:s1:"+strconv.FormatInt(t0.UnixMilli(), 10) {
		t.Fatalf("event %+v", e)
	}
	// The live row moves with each turn; at its time it fires once.
	if err := d.Arm(ctx, "telegram:42", "", "telegram", owner, t0.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(15 * time.Minute)
	if err := timer.Tick(ctx); err != nil || len(p.got) != 1 {
		t.Fatalf("fired before its time: %+v %v", p.got, err)
	}
	now = t0.Add(21 * time.Minute)
	if err := timer.Tick(ctx); err != nil || len(p.got) != 2 {
		t.Fatalf("not fired: %+v %v", p.got, err)
	}
	if err := timer.Tick(ctx); err != nil || len(p.got) != 2 {
		t.Fatalf("fired twice: %+v %v", p.got, err)
	}
}

// A row armed again between the put and the removal (a turn during the
// tick) is kept for its new time.
func TestReviewTimerKeepsARowArmedAgain(t *testing.T) {
	ctx := context.Background()
	d := reviewDue(t)
	if err := d.Arm(ctx, "telegram:42", "", "telegram", owner, t0); err != nil {
		t.Fatal(err)
	}
	put := func(ctx context.Context, _ event.Event) error {
		return d.Arm(ctx, "telegram:42", "", "telegram", owner, t0.Add(10*time.Minute))
	}
	timer := NewReviewTimer(d, put, func() time.Time { return t0 }, time.Second, discardLog())
	if err := timer.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := d.due(ctx, t0.Add(time.Hour))
	if err != nil || len(rows) != 1 || !rows[0].Due.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("rows %+v %v", rows, err)
	}
}

// A put that fails leaves the row for the next tick.
func TestReviewTimerRetriesAFailedPut(t *testing.T) {
	ctx := context.Background()
	d := reviewDue(t)
	if err := d.Arm(ctx, "telegram:42", "", "telegram", owner, t0); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, event.Event) error { return context.DeadlineExceeded }
	if err := NewReviewTimer(d, fail, func() time.Time { return t0 }, time.Second, discardLog()).Tick(ctx); err == nil {
		t.Fatal("a failed put is not an error")
	}
	if rows, _ := d.due(ctx, t0); len(rows) != 1 {
		t.Fatalf("the row is gone: %+v", rows)
	}
}
