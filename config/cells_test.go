package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/samber/do/v2"
)

// seedOldCells puts a cell into the database's cells table, as a node of an
// earlier version kept it.
func seedOldCells(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	old, err := sqlstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	c := cells.Cell{ID: "demo-reader.soc", Kind: cells.KindSensor, Version: 4, Updated: at, Owner: "demo-reader"}
	if _, err := old.Create(ctx, c, "old"); err != nil {
		t.Fatal(err)
	}
}

// A node whose database holds cells and whose bucket is empty copies them
// into the bucket at its first start, and a second start copies nothing.
func TestCellsMoveFromTheDatabaseToTheBucketOnce(t *testing.T) {
	b := testConfig(t)
	b.BusDir = bus.StorePath(filepath.Dir(b.DB))
	seedOldCells(t, b.DB)
	for start := range 2 {
		i := New(context.Background(), b, slog.New(slog.DiscardHandler))
		svc := do.MustInvoke[*cells.Service](i)
		got, err := svc.Get(context.Background(), "demo-reader.soc")
		if err != nil || got.Version != 4 || got.Owner != "demo-reader" {
			t.Fatalf("start %d: cell = %+v, %v", start, got, err)
		}
		h, err := svc.History(context.Background(), "demo-reader.soc", 10)
		if err != nil || len(h) != 1 || h[0].Source != cells.MigrationSource {
			t.Fatalf("start %d: history = %+v, %v; want the one change of the migration", start, h, err)
		}
		if rep := i.Shutdown(); !rep.Succeed {
			t.Fatalf("shutdown: %v", rep.Error())
		}
	}
}
