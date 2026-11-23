package config

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/secrets/vault"
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
	b.MasterKey = vault.KeyPath(b.DB) // the bus's chain lives in the store, which must open again
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

// A restore made into a node that already has cells stops the start with an
// error that says so, and keeps the file.
func TestRestoredCellsAreRefusedByABucketThatHoldsCells(t *testing.T) {
	ctx := context.Background()
	b := testConfig(t)
	b.BusDir = bus.StorePath(filepath.Dir(b.DB))
	b.MasterKey = vault.KeyPath(b.DB) // the bus's chain lives in the store, which must open again
	i := New(ctx, b, slog.New(slog.DiscardHandler))
	if err := do.MustInvoke[*cells.Service](i).Define(ctx, cells.Def{ID: "lamp", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	if rep := i.Shutdown(); !rep.Succeed {
		t.Fatalf("shutdown: %v", rep.Error())
	}
	path := backup.ImportPath(filepath.Dir(b.DB))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const export = `{"key":"other","seq":1,"op":"put","value":"e30="}` + "\n"
	if err := os.WriteFile(path, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}

	j := New(ctx, b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = j.Shutdown() })
	_, err := do.Invoke[*cells.Service](j)
	if !errors.Is(err, natsbus.ErrNotEmpty) || !strings.Contains(err.Error(), "holds 1 entries") {
		t.Fatalf("error %v", err)
	}
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != export {
		t.Fatalf("the file: %q, %v", got, rerr)
	}
}
