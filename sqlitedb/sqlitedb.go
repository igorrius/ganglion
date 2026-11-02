// Package sqlitedb opens SQLite databases with the settings every ganglion
// component uses and applies per-component schema migrations (ADR-0016).
//
// Each package that owns tables embeds its migrations as NNN_name.sql files
// and calls Migrate with its own component name, so packages evolve their
// schemas independently in one database file.
package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no CGO
)

// Open opens (creating if needed) the SQLite database at path with WAL,
// a busy timeout, foreign keys and synchronous=FULL: a commit survives a
// power loss, which the inbox relies on once Telegram has been told the
// update is stored (R-DLV-2). The special path
// ":memory:" gives a private in-memory database on a single connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// Every new connection would get its own empty in-memory database.
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitedb: open %s: %w", path, err)
	}
	return db, nil
}

var migrationName = regexp.MustCompile(`^(\d{3,})_[a-z0-9_]+\.sql$`)

// Migrate applies the migrations in fsys (top-level files named
// NNN_name.sql) that component has not applied yet, in numeric order, each
// in its own transaction. Applied versions are recorded in
// schema_migrations.
func Migrate(ctx context.Context, db *sql.DB, component string, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component  TEXT    NOT NULL,
		version    INTEGER NOT NULL,
		applied_ms INTEGER NOT NULL DEFAULT (CAST(unixepoch('subsec') * 1000 AS INTEGER)),
		PRIMARY KEY (component, version))`); err != nil {
		return fmt.Errorf("sqlitedb: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("sqlitedb: read migrations of %s: %w", component, err)
	}
	type migration struct {
		version int
		name    string
	}
	var todo []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return fmt.Errorf("sqlitedb: %s: migration %q is not named NNN_name.sql", component, e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		todo = append(todo, migration{version: v, name: e.Name()})
	}
	slices.SortFunc(todo, func(a, b migration) int { return a.version - b.version })

	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations WHERE component = ?`, component,
	).Scan(&current); err != nil {
		return fmt.Errorf("sqlitedb: read version of %s: %w", component, err)
	}
	for _, m := range todo {
		if m.version <= current {
			continue
		}
		body, err := fs.ReadFile(fsys, m.name)
		if err != nil {
			return err
		}
		if err := apply(ctx, db, component, m.version, string(body)); err != nil {
			return fmt.Errorf("sqlitedb: %s migration %s: %w", component, m.name, err)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, component string, version int, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (component, version) VALUES (?, ?)`, component, version); err != nil {
		return err
	}
	return tx.Commit()
}
