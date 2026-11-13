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
	"database/sql/driver"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no CGO
)

// Open opens (creating if needed) the SQLite database at path with WAL,
// a busy timeout, foreign keys and synchronous=FULL: a commit survives a
// power loss, which the inbox relies on once Telegram has been told the
// update is stored (R-DLV-2). The special path
// ":memory:" gives a private in-memory database on a single connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := "file:" + uriPath(path) +
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

// uriPath escapes the characters that end or change the path of a SQLite
// file: URI, so a path holding "?", "#" or "%" names that file and cannot
// add parameters of its own.
func uriPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// OpenReadOnly opens the existing database at path for reading only
// (mode=ro and query_only): diagnostics read beside a running core
// without taking its lock or changing anything, and never run migrations
// (R-OBS-7, ADR-0044). A missing file is an error.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+uriPath(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitedb: open %s read-only: %w", path, err)
	}
	return db, nil
}

var migrationName = regexp.MustCompile(`^(\d{3,})_[a-z0-9_]+\.sql$`)

// Migrate applies the migrations in fsys (top-level files named
// NNN_name.sql) that component has not applied yet, in numeric order, each
// in its own transaction. Applied versions are recorded in
// schema_migrations. Two processes may migrate one component at once:
// each migration is applied by one of them.
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

// apply runs one migration and records it, under the write lock taken
// first: a second process that migrates the same component at the same
// time (ganglion secret beside a starting node, ADR-0077) waits, then
// finds the version recorded and applies nothing.
func apply(ctx context.Context, db *sql.DB, component string, version int, body string) error {
	return Immediate(ctx, db, func(c *sql.Conn) error {
		var applied bool
		if err := c.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE component = ? AND version = ?)`,
			component, version).Scan(&applied); err != nil || applied {
			return err
		}
		if _, err := c.ExecContext(ctx, body); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx,
			`INSERT INTO schema_migrations (component, version) VALUES (?, ?)`, component, version)
		return err
	})
}

// Immediate runs fn in a BEGIN IMMEDIATE transaction on one connection, so
// the write lock is taken before fn reads: two callers cannot both pick
// the same row. fn's error rolls the transaction back.
func Immediate(ctx context.Context, db *sql.DB, fn func(c *sql.Conn) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		if _, rbErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); rbErr != nil {
			// The connection may still be inside a transaction: drop it
			// instead of returning it to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
