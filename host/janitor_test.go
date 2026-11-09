package host

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

func TestSweepRemovesOnlyOldEntriesOfAgedPlaces(t *testing.T) {
	tmp, keep, now := t.TempDir(), t.TempDir(), time.Now()
	old, fresh := now.Add(-25*time.Hour), now.Add(-time.Hour)
	touch(t, filepath.Join(tmp, "ganglion-claude-1", "x"), 10, old)
	if err := os.Chtimes(filepath.Join(tmp, "ganglion-claude-1"), old, old); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(tmp, "ganglion-claude-2"), 10, fresh)
	touch(t, filepath.Join(tmp, "someone-else"), 10, old)
	touch(t, filepath.Join(keep, "ancient.log"), 10, old)
	j := NewJanitor([]Place{
		{Name: PlaceTemp, Path: tmp, Prefix: "ganglion-claude-", MaxAge: 24 * time.Hour},
		{Name: PlaceLogs, Path: keep}, // no age: never swept
	}, slog.New(slog.DiscardHandler), WithJanitorClock(func() time.Time { return now }, nil))
	n, bytes := j.Sweep(context.Background())
	if n != 1 || bytes != 10 {
		t.Fatalf("swept %d entries, %d bytes; want 1, 10", n, bytes)
	}
	for _, p := range []string{"ganglion-claude-2", "someone-else"} {
		if _, err := os.Lstat(filepath.Join(tmp, p)); err != nil {
			t.Fatalf("%s was removed: %v", p, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(keep, "ancient.log")); err != nil {
		t.Fatal("a place without an age was swept")
	}
}

func TestSweepCountsCleanedBytesByPlace(t *testing.T) {
	rec := telemetrytest.Install(t)
	dir, now := t.TempDir(), time.Now()
	old := now.Add(-48 * time.Hour)
	touch(t, filepath.Join(dir, "ganglion-mcp-1", "x"), 7, old)
	if err := os.Chtimes(filepath.Join(dir, "ganglion-mcp-1"), old, old); err != nil {
		t.Fatal(err)
	}
	j := NewJanitor([]Place{{Name: PlaceTemp, Path: dir, Prefix: "ganglion-mcp-", MaxAge: time.Hour}}, slog.New(slog.DiscardHandler),
		WithJanitorClock(func() time.Time { return now }, nil))
	j.Sweep(context.Background())
	if got := rec.Value(t, "ganglion.host.cleaned", map[string]string{"place": string(PlaceTemp)}); got != 7 {
		t.Fatalf("ganglion.host.cleaned = %v, want 7", got)
	}
}

func TestRunSweepsAtStartThenEveryHour(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	touch(t, filepath.Join(dir, "a"), 1, now.Add(-48*time.Hour))
	waits := make(chan time.Duration)
	tick := make(chan time.Time)
	j := NewJanitor([]Place{{Name: PlaceClaude, Path: dir, MaxAge: time.Hour}}, slog.New(slog.DiscardHandler),
		WithJanitorClock(func() time.Time { return now }, func(d time.Duration) <-chan time.Time { waits <- d; return tick }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- j.Run(ctx) }()
	if d := <-waits; d != SweepEvery {
		t.Fatalf("waited %v, want %v", d, SweepEvery)
	}
	if _, err := os.Lstat(filepath.Join(dir, "a")); err == nil {
		t.Fatal("the first sweep did not run at start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSweepKeepsTheFolderOfAPlaceWithoutPrefix(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	old := now.Add(-48 * time.Hour)
	touch(t, filepath.Join(dir, "run-1"), 3, old)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	j := NewJanitor([]Place{{Name: PlaceClaude, Path: dir, MaxAge: time.Hour}}, slog.New(slog.DiscardHandler),
		WithJanitorClock(func() time.Time { return now }, nil))
	if n, _ := j.Sweep(context.Background()); n != 1 {
		t.Fatalf("swept %d entries, want 1", n)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the place's own folder was removed")
	}
}
