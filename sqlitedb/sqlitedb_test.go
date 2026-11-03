package sqlitedb

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func openFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func TestOpenSetsWAL(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, openFile(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (%v), want 1", fk, err)
	}
	// FULL: a commit survives a power loss, not only a process kill. A
	// message confirmed to Telegram must not roll back (R-DLV-2).
	var sync int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("synchronous = %d (%v), want 2 (FULL)", sync, err)
	}
}

func TestMigrateAppliesInOrderOnce(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fs := fstest.MapFS{
		"002_add_column.sql": {Data: []byte("ALTER TABLE t ADD COLUMN b TEXT;")},
		"001_create.sql":     {Data: []byte("CREATE TABLE t (a INTEGER);")},
	}
	for i := range 2 {
		if err := Migrate(ctx, db, "x", fs); err != nil {
			t.Fatalf("Migrate run %d: %v", i+1, err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t (a, b) VALUES (1, 'x')"); err != nil {
		t.Fatalf("schema after migrations: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE component = 'x'").Scan(&n); err != nil || n != 2 {
		t.Fatalf("recorded migrations = %d (%v), want 2", n, err)
	}
}

func TestMigrateComponentsAreIndependent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := Migrate(ctx, db, "a", fstest.MapFS{"001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, "b", fstest.MapFS{"001_b.sql": {Data: []byte("CREATE TABLE b (x INTEGER);")}}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestMigrateFailedFileRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fs := fstest.MapFS{
		"001_ok.sql":     {Data: []byte("CREATE TABLE ok (a INTEGER);")},
		"002_broken.sql": {Data: []byte("CREATE TABLE half (a INTEGER); THIS IS NOT SQL;")},
	}
	if err := Migrate(ctx, db, "x", fs); err == nil {
		t.Fatal("Migrate with a broken file succeeded")
	}
	var v int
	if err := db.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations WHERE component = 'x'").Scan(&v); err != nil || v != 1 {
		t.Fatalf("recorded version = %d (%v), want 1", v, err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO half VALUES (1)"); err == nil {
		t.Fatal("table from the failed migration exists")
	}
}

func TestMigrateRejectsBadNames(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, name := range []string{"x.sql", "1_a.txt", "abc_x.sql"} {
		if err := Migrate(ctx, db, "x", fstest.MapFS{name: {Data: []byte("SELECT 1;")}}); err == nil {
			t.Errorf("Migrate accepted %q", name)
		}
	}
}

// R-OBS-7: diagnostics open the database read-only; a write fails and
// nothing is created or migrated.
func TestReadOnlyRefusesWrites(t *testing.T) {
	ctx := context.Background()
	path := openFile(t)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if _, err := ro.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err == nil {
		t.Fatal("a write through the read-only handle succeeded")
	}
	var n int
	if err := ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("read: %d, %v", n, err)
	}
	if _, err := OpenReadOnly(ctx, filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("a missing database opened")
	}
	_ = db.Close()
}
