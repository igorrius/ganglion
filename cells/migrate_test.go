package cells_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

func TestMigrateCopiesIntoAnEmptyStoreOnce(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	from, to := cells.NewMemStore(), cells.NewMemStore()
	for _, id := range []string{"a", "b"} {
		c := cells.Cell{ID: id, Kind: cells.KindSensor, Version: 7, Updated: at, Owner: "m", About: "x"}
		if _, err := from.Create(ctx, c, "old"); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.DiscardHandler)
	n, err := cells.Migrate(ctx, from, to, log)
	if err != nil || n != 2 {
		t.Fatalf("first = %d, %v; want 2", n, err)
	}
	c, _ := to.Get(ctx, "a")
	if c.Version != 7 || c.Owner != "m" || c.About != "x" {
		t.Fatalf("copied %+v", c)
	}
	h, _ := to.History(ctx, "a", 10)
	if len(h) != 1 || h[0].Source != cells.MigrationSource {
		t.Fatalf("history = %+v, want one change from migration", h)
	}
	if n, err := cells.Migrate(ctx, from, to, log); err != nil || n != 0 {
		t.Fatalf("second = %d, %v; want 0", n, err)
	}
}

func TestMigrateLeavesAStoreWithCells(t *testing.T) {
	ctx := context.Background()
	from, to := cells.NewMemStore(), cells.NewMemStore()
	_, _ = from.Create(ctx, cells.Cell{ID: "old", Kind: cells.KindSensor, Version: 1}, "old")
	_, _ = to.Create(ctx, cells.Cell{ID: "new", Kind: cells.KindSensor, Version: 1}, "test")
	if n, err := cells.Migrate(ctx, from, to, slog.New(slog.DiscardHandler)); err != nil || n != 0 {
		t.Fatalf("= %d, %v; want 0", n, err)
	}
	if _, err := to.Get(ctx, "old"); err == nil {
		t.Fatal("a cell was copied into a store that had cells")
	}
}
