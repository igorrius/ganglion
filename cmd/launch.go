package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/update"
)

// releasesDir is the folder of the node's core releases beside the
// database at db. ok is false when the node has none: a database in memory
// or no state.json, which is a node that never updated. The folder is
// never created here.
func releasesDir(db string) (dir string, ok bool) {
	if db == memoryDB {
		return "", false
	}
	dir = filepath.Join(filepath.Dir(db), "releases")
	if _, err := os.Stat(filepath.Join(dir, update.StateFile)); err != nil {
		return "", false
	}
	return dir, true
}

// launch is the first step of `run`: it moves the process onto the active
// release, or back to the previous one (R-UPD-4, R-UPD-5). It returns when
// this process is the one to run. Its own failure never stops the node:
// the line goes to errOut and the node runs as it is, the floor.
func launch(ctx context.Context, db string, errOut io.Writer) {
	dir, ok := releasesDir(db)
	if !ok {
		return
	}
	if err := runLauncher(ctx, dir, db, errOut); err != nil {
		_, _ = fmt.Fprintf(errOut, "update: the launcher failed; starting this binary as it is: %v\n", err)
	}
}

func runLauncher(ctx context.Context, dir, db string, errOut io.Writer) error {
	store, err := update.Open(dir)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return update.Launcher{Store: store, DB: db, Self: self, Migrations: migrations, Exec: execBinary, Log: errOut}.Launch(ctx)
}

// cleanStop takes back the start attempt of the running version after a
// restart it was asked to make (ADR-0046). It never fails the stop.
func cleanStop(db string, in buildinfo.Info, errOut io.Writer) {
	dir, ok := releasesDir(db)
	if !ok {
		return
	}
	store, err := update.Open(dir)
	if err == nil {
		err = store.CleanStop(in.Version)
	}
	if err != nil && !errors.Is(err, update.ErrBusy) {
		_, _ = fmt.Fprintf(errOut, "update: the start attempt was not taken back: %v\n", err)
	}
}

// migrations lists the migrations the database file at path has applied,
// as "component:version", read-only so a running node's file is not
// touched. A database without the table has none.
func migrations(ctx context.Context, path string) ([]string, error) {
	db, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&n); err != nil || n == 0 {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT component, version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	return scanMigrations(rows)
}

func scanMigrations(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var component string
		var version int64
		if err := rows.Scan(&component, &version); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s:%d", component, version))
	}
	return out, rows.Err()
}
