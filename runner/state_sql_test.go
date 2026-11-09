package runner_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sqlitedb"
)

func TestSQLStatePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// modstore owns the module_state migration.
	if _, err := modstore.Open(ctx, db, t.TempDir(), time.Now, runner.Deps{}); err != nil {
		t.Fatal(err)
	}

	runner.NewSQLState(db).Put("probe", "token", "abc")
	runner.NewSQLState(db).Put("probe", "token", "abd") // an overwrite, not a second row
	runner.NewSQLState(db).Put("other", "token", "zzz")

	fresh := runner.NewSQLState(db)
	if v, ok := fresh.Get("probe", "token"); !ok || v != "abd" {
		t.Fatalf("Get(probe, token) = %q, %v; want abd", v, ok)
	}
	if v, ok := fresh.Get("other", "token"); !ok || v != "zzz" {
		t.Fatalf("Get(other, token) = %q, %v; want zzz", v, ok)
	}
	if _, ok := fresh.Get("probe", "missing"); ok {
		t.Fatal("a missing key was found")
	}
}
