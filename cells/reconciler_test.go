package cells_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/virtual"
)

// The reconciler acts on out-of-sync switches at start and then every
// period, logs a failed pass and goes on, and ends with its context
// (design section 8.5).
func TestReconcilerRunsAtStartAndEachPeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newStore(t)
	before := cells.NewService(store, cells.WithBackend("virtual", crashing{}))
	if err := before.Define(ctx, cells.Def{ID: "load.a", Kind: cells.KindSwitch, Backend: "virtual"}); err != nil {
		t.Fatal(err)
	}
	_, _ = before.SetDesired(ctx, "load.a", on, "flow", 0)
	s := cells.NewService(store, cells.WithBackend("virtual", virtual.New()))

	ticks := make(chan time.Time)
	waited := make(chan time.Duration, 4)
	var logs bytes.Buffer
	r := cells.Reconciler{Service: s, Every: 5 * time.Minute, Log: slog.New(slog.NewTextHandler(&logs, nil)),
		After: func(d time.Duration) <-chan time.Time { waited <- d; return ticks }}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	if d := <-waited; d != 5*time.Minute {
		t.Fatalf("waited %v", d)
	}
	if a, _ := s.Get(ctx, "load.a"); !a.InSync() {
		t.Fatalf("load.a not reconciled at start: %+v", a)
	}
	ticks <- time.Time{}
	<-waited
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if r.Name() != "reconcile-cells" || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("name %s, logs %s", r.Name(), logs.String())
	}
}
