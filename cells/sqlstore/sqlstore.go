// Package sqlstore is a cells.Store on SQLite (one table for cells, one for
// the change log). Open the database with sqlitedb.Open.
package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a cells.Store backed by SQLite.
type Store struct {
	db *sql.DB
}

// New migrates the "cells" component and returns the store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "cells", sub); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

const cellColumns = `id, kind, backend, desired, reported, reported_cause, version, updated_ms, expires_ms, owner, about`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func raw(b json.RawMessage) any {
	if b == nil {
		return nil
	}
	return string(b)
}

type scanner interface{ Scan(dest ...any) error }

func scanCell(r scanner) (cells.Cell, error) {
	var (
		c                  cells.Cell
		kind, cause        string
		desired, reported  sql.NullString
		updated, expiresMS int64
	)
	if err := r.Scan(&c.ID, &kind, &c.Backend, &desired, &reported, &cause, &c.Version, &updated, &expiresMS, &c.Owner, &c.About); err != nil {
		return cells.Cell{}, err
	}
	c.Kind, c.ReportedCause = cells.Kind(kind), cells.Cause(cause)
	if desired.Valid {
		c.Desired = json.RawMessage(desired.String)
	}
	if reported.Valid {
		c.Reported = json.RawMessage(reported.String)
	}
	c.Updated, c.Expires = fromMS(updated), fromMS(expiresMS)
	return c, nil
}

func insertChange(ctx context.Context, tx *sql.Tx, ch cells.Change) (cells.Change, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO cell_changes (cell_id, version, field, value, cause, source, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ch.CellID, ch.Version, ch.Field, raw(ch.Value), string(ch.Cause), ch.Source, ms(ch.At))
	if err != nil {
		return cells.Change{}, err
	}
	ch.Seq, err = res.LastInsertId()
	return ch, err
}

// Create implements cells.Store.
func (s *Store) Create(ctx context.Context, c cells.Cell, src string) (cells.Change, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cells.Change{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO cells (`+cellColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, string(c.Kind), c.Backend, raw(c.Desired), raw(c.Reported), string(c.ReportedCause), c.Version, ms(c.Updated), ms(c.Expires), c.Owner, c.About)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return cells.Change{}, cells.ErrExists
		}
		return cells.Change{}, fmt.Errorf("sqlstore: create %s: %w", c.ID, err)
	}
	ch, err := insertChange(ctx, tx, cells.Change{CellID: c.ID, Version: c.Version, Field: cells.FieldCreated, Source: src, At: c.Updated})
	if err != nil {
		return cells.Change{}, err
	}
	return ch, tx.Commit()
}

// Get implements cells.Store.
func (s *Store) Get(ctx context.Context, id string) (cells.Cell, error) {
	c, err := scanCell(s.db.QueryRowContext(ctx, `SELECT `+cellColumns+` FROM cells WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return cells.Cell{}, cells.ErrNotFound
	}
	return c, err
}

func (s *Store) queryCells(ctx context.Context, query string, args ...any) ([]cells.Cell, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []cells.Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// List implements cells.Store.
func (s *Store) List(ctx context.Context) ([]cells.Cell, error) {
	return s.queryCells(ctx, `SELECT `+cellColumns+` FROM cells ORDER BY id`)
}

// Update implements cells.Store.
func (s *Store) Update(ctx context.Context, c cells.Cell, prevVersion int64, ch cells.Change) (cells.Change, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cells.Change{}, err
	}
	defer func() { _ = tx.Rollback() }()
	c.Version = prevVersion + 1
	res, err := tx.ExecContext(ctx, `UPDATE cells SET kind = ?, backend = ?, desired = ?, reported = ?, reported_cause = ?,
		version = ?, updated_ms = ?, expires_ms = ? WHERE id = ? AND version = ?`,
		string(c.Kind), c.Backend, raw(c.Desired), raw(c.Reported), string(c.ReportedCause),
		c.Version, ms(c.Updated), ms(c.Expires), c.ID, prevVersion)
	if err != nil {
		return cells.Change{}, fmt.Errorf("sqlstore: update %s: %w", c.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Check inside the transaction: on a single-connection database
		// (":memory:") a second query through s.db would wait forever.
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM cells WHERE id = ?`, c.ID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return cells.Change{}, cells.ErrNotFound
		}
		if err != nil {
			return cells.Change{}, err
		}
		return cells.Change{}, cells.ErrConflict
	}
	ch.CellID, ch.Version = c.ID, c.Version
	ch, err = insertChange(ctx, tx, ch)
	if err != nil {
		return cells.Change{}, err
	}
	return ch, tx.Commit()
}

func (s *Store) queryChanges(ctx context.Context, query string, args ...any) ([]cells.Change, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []cells.Change
	for rows.Next() {
		var (
			ch    cells.Change
			value sql.NullString
			cause string
			at    int64
		)
		if err := rows.Scan(&ch.Seq, &ch.CellID, &ch.Version, &ch.Field, &value, &cause, &ch.Source, &at); err != nil {
			return nil, err
		}
		if value.Valid {
			ch.Value = json.RawMessage(value.String)
		}
		ch.Cause, ch.At = cells.Cause(cause), fromMS(at)
		out = append(out, ch)
	}
	return out, rows.Err()
}

const changeColumns = `seq, cell_id, version, field, value, cause, source, at_ms`

// ChangesSince implements cells.Store.
func (s *Store) ChangesSince(ctx context.Context, seq int64, limit int) ([]cells.Change, error) {
	return s.queryChanges(ctx, `SELECT `+changeColumns+` FROM cell_changes WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit)
}

// History implements cells.Store.
func (s *Store) History(ctx context.Context, id string, limit int) ([]cells.Change, error) {
	return s.queryChanges(ctx, `SELECT `+changeColumns+` FROM cell_changes WHERE cell_id = ? ORDER BY seq DESC LIMIT ?`, id, limit)
}

// HistorySince implements cells.Store.
func (s *Store) HistorySince(ctx context.Context, id string, sinceMS int64, limit int) ([]cells.Change, error) {
	return s.queryChanges(ctx, `SELECT `+changeColumns+` FROM cell_changes WHERE cell_id = ? AND at_ms >= ? ORDER BY seq LIMIT ?`, id, sinceMS, limit)
}

// SetOwner implements cells.Store.
func (s *Store) SetOwner(ctx context.Context, id, owner, about string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE cells SET owner = ?, about = ? WHERE id = ?`, owner, about, id)
	if err != nil {
		return fmt.Errorf("sqlstore: set owner of %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return cells.ErrNotFound
	}
	return nil
}

// Disown implements cells.Store.
func (s *Store) Disown(ctx context.Context, owner string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE cells SET owner = '' WHERE owner = ?`, owner)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: disown %s: %w", owner, err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PendingRequestsBefore implements cells.Store.
func (s *Store) PendingRequestsBefore(ctx context.Context, t time.Time) ([]cells.Cell, error) {
	return s.queryCells(ctx, `SELECT `+cellColumns+` FROM cells
		WHERE kind = ? AND json_extract(reported, '$.status') = ? AND expires_ms > 0 AND expires_ms <= ?
		ORDER BY id`, string(cells.KindRequest), cells.StatusPending, t.UnixMilli())
}
