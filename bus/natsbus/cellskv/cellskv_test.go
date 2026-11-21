package cellskv_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/bus/natsbus/cellskv"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/storetest"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// open starts an embedded bus with an empty store folder and returns a
// store on its cells bucket.
func open(t *testing.T) *cellskv.Store {
	t.Helper()
	ctx := context.Background()
	b, err := natsbus.Open(ctx, natsbus.DefaultConfig(""), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := cellskv.New(ctx, b.JetStream())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sw(id string) cells.Cell {
	return cells.Cell{ID: id, Kind: cells.KindSwitch, Version: 1, Updated: t0}
}

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) cells.Store { return open(t) })
}

// Ids with characters NATS refuses in a key, or that the escape itself
// uses, each keep their own cell.
func TestKeyEscaping(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	ids := []string{"a:b", "a=3Ab", "a=b", ".a", "a.", "a..b", "a b", "ключ", "x/y", "A.b"}
	for _, id := range ids {
		if _, err := s.Create(ctx, sw(id), "test"); err != nil {
			t.Fatalf("create %q: %v", id, err)
		}
	}
	for _, id := range ids {
		c, err := s.Get(ctx, id)
		if err != nil || c.ID != id {
			t.Fatalf("get %q = %+v, %v", id, c, err)
		}
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != len(ids) {
		t.Fatalf("list = %d cells (%v), want %d", len(all), err, len(ids))
	}
}

// Of several writers that read the same version, one wins and the rest get
// ErrConflict.
func TestLostRaceIsAConflict(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.Create(ctx, sw("load.a"), "test"); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			c := sw("load.a")
			c.Desired = json.RawMessage(`"on"`)
			_, err := s.Update(ctx, c, 1, cells.Change{Field: cells.FieldDesired, Value: c.Desired, Source: "test", At: t0})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, cells.ErrConflict):
			lost++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || lost != writers-1 {
		t.Fatalf("won %d, lost %d; want 1 and %d", won, lost, writers-1)
	}
	if c, _ := s.Get(ctx, "load.a"); c.Version != 2 {
		t.Fatalf("version = %d, want 2", c.Version)
	}
}

// ChangesSince merges the changes of several cells in one global order, and
// leaves out the writes that made no change.
func TestChangesSinceAcrossCells(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, id := range []string{"c.a", "c.b", "c.c"} {
		if _, err := s.Create(ctx, sw(id), "test"); err != nil {
			t.Fatal(err)
		}
	}
	for v := int64(1); v <= 3; v++ {
		for _, id := range []string{"c.c", "c.a", "c.b"} {
			c := sw(id)
			if _, err := s.Update(ctx, c, v, cells.Change{Field: cells.FieldReported, Value: json.RawMessage(`"on"`), At: t0}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetOwner(ctx, "c.a", "m", "about"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ChangesSince(ctx, 0, 1000)
	if err != nil || len(all) != 12 {
		t.Fatalf("all = %d changes (%v), want 12", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("out of order at %d: %+v", i, all)
		}
	}
	rest, err := s.ChangesSince(ctx, all[5].Seq, 1000)
	if err != nil || len(rest) != 6 || rest[0].Seq != all[6].Seq {
		t.Fatalf("rest = %+v (%v)", rest, err)
	}
	if page, _ := s.ChangesSince(ctx, 0, 5); len(page) != 5 || page[4].Seq != all[4].Seq {
		t.Fatalf("page = %+v", page)
	}
	if none, err := s.ChangesSince(ctx, all[11].Seq, 10); err != nil || len(none) != 0 {
		t.Fatalf("after the last = %+v (%v)", none, err)
	}
}

// More than one batch of messages is read through.
func TestChangesSinceReadsPastABatch(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	const cellCount = 300
	for i := range cellCount {
		id := "bulk." + string(rune('a'+i/26)) + string(rune('a'+i%26))
		if _, err := s.Create(ctx, sw(id), "test"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ChangesSince(ctx, 0, 1000)
	if err != nil || len(all) != cellCount {
		t.Fatalf("got %d changes (%v), want %d", len(all), err, cellCount)
	}
}

// The history of a key is what the bucket keeps: the newest 64 values.
func TestHistoryIsBoundedByTheBucket(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.Create(ctx, sw("load.a"), "test"); err != nil {
		t.Fatal(err)
	}
	for v := int64(1); v <= 70; v++ {
		if _, err := s.Update(ctx, sw("load.a"), v, cells.Change{Field: cells.FieldReported, Value: json.RawMessage(`"on"`), At: t0}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.History(ctx, "load.a", 100)
	if err != nil || len(h) != 64 || h[0].Version != 71 {
		t.Fatalf("history = %d entries, newest version %d (%v), want 64 and 71", len(h), h[0].Version, err)
	}
}
