package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/sqlitedb"
)

type crashingStore struct {
	cells.Store
	limit int
}

func (f *crashingStore) Create(ctx context.Context, c cells.Cell, src string) (cells.Change, error) {
	if f.limit == 0 {
		return cells.Change{}, errors.New("crash")
	}
	f.limit--
	return f.Store.Create(ctx, c, src)
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateToBusFinishesAfterACrashAndRenamesTheTables(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	old, err := sqlstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := old.Create(ctx, cells.Cell{ID: id, Kind: cells.KindSensor, Version: 3, Updated: at}, "old"); err != nil {
			t.Fatal(err)
		}
	}
	to := cells.NewMemStore()
	if _, err := sqlstore.MigrateToBus(ctx, db, &crashingStore{Store: to, limit: 1}, log); err == nil {
		t.Fatal("the crash was not seen")
	}
	if !tableExists(t, db, "cells") {
		t.Fatal("the table was renamed before the copy was done")
	}
	got, _ := to.Get(ctx, "a") // copied before the crash, then written again
	got.Reported = []byte(`"newer"`)
	if _, err := to.Update(ctx, got, got.Version, cells.Change{Field: cells.FieldReported, Source: "test", At: at}); err != nil {
		t.Fatal(err)
	}
	n, err := sqlstore.MigrateToBus(ctx, db, to, log)
	if err != nil || n != 2 {
		t.Fatalf("rerun = %d, %v; want 2", n, err)
	}
	if all, _ := to.List(ctx); len(all) != 3 {
		t.Fatalf("%d cells, want 3", len(all))
	}
	if a, _ := to.Get(ctx, "a"); string(a.Reported) != `"newer"` {
		t.Fatalf("the rerun overwrote a: %+v", a)
	}
	if tableExists(t, db, "cells") || !tableExists(t, db, "cells_migrated") || !tableExists(t, db, "cell_changes_migrated") {
		t.Fatal("the tables were not renamed")
	}
	if n, err := sqlstore.MigrateToBus(ctx, db, to, log); err != nil || n != 0 {
		t.Fatalf("third start = %d, %v; want 0", n, err)
	}
	if tableExists(t, db, "cells") {
		t.Fatal("the cells table was made again")
	}
}

// A node that never had the table gets none.
func TestMigrateToBusDoesNotMakeTheTable(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if n, err := sqlstore.MigrateToBus(ctx, db, cells.NewMemStore(), slog.New(slog.DiscardHandler)); err != nil || n != 0 {
		t.Fatalf("= %d, %v", n, err)
	}
	if tableExists(t, db, "cells") {
		t.Fatal("the table was made")
	}
}
