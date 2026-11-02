package sqlqueue_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/queue/sqlqueue"
)

func TestPrunerPrunesAtStartAndStopsWithTheContext(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	put(t, f, msg("telegram:1", "done"))
	if err := f.q.Complete(ctx, lease(t, f), nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(sqlqueue.Retention + time.Hour)

	p := sqlqueue.Pruner{Queue: f.q, Every: time.Hour, Log: slog.New(slog.DiscardHandler)}
	if p.Name() != "prune" {
		t.Errorf("Name() = %q", p.Name())
	}
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM inbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the finished row was not pruned at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil when the context ends", err)
	}
}

// The process that owns the database releases the leases a dead process
// left (ADR-0019), so their events are leasable at once.
func TestOpenOwnedReleasesDeadLeases(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "held"))
	lease(t, f) // a process took it and died
	q, err := sqlqueue.OpenOwned(context.Background(), f.db, slog.New(slog.DiscardHandler), sqlqueue.WithClock(f.clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	if l, err := q.Lease(context.Background(), time.Minute); err != nil || l.Event.IdemKey != "held" {
		t.Fatalf("lease after OpenOwned: %+v, %v", l, err)
	}
}
