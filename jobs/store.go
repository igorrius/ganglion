package jobs

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store keeps the background jobs' rows in the node's database, so a restart
// finds the jobs it left open and edits their cards once more.
type Store struct{ db *sql.DB }

// NewStore migrates the "jobs" component and returns the store.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "jobs", sub); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// row is a job as stored, with its card: the last edit's number and its
// text, "" until its first message is in the outbox.
type row struct {
	job     Job
	version int
	shown   string
}

// cardColumns are a row's columns in the order scanCard reads them.
const cardColumns = `key, kind, conv, transport, principal, title, state, count, started_ms, ended_ms, version, shown`

// rowScanner is a *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanCard reads one row of cardColumns.
func scanCard(s rowScanner) (row, error) {
	var (
		c              row
		kind, state    string
		started, ended int64
	)
	if err := s.Scan(&c.job.Key, &kind, &c.job.Conv, &c.job.Transport, &c.job.Principal, &c.job.Title, &state,
		&c.job.Count, &started, &ended, &c.version, &c.shown); err != nil {
		return row{}, err
	}
	c.job.Kind, c.job.State, c.job.Started = Kind(kind), State(state), time.UnixMilli(started).UTC()
	if ended != 0 {
		c.job.Ended = time.UnixMilli(ended).UTC()
	}
	return c, nil
}

// known reports whether key has a row, open or ended.
func (s *Store) known(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM background_jobs WHERE key = ?`, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: read %s: %w", key, err)
	}
	return true, nil
}

// insert stores a new job with its first card.
func (s *Store) insert(ctx context.Context, c row) error {
	j := c.job
	if _, err := s.db.ExecContext(ctx, `INSERT INTO background_jobs (key, kind, conv, transport, principal, title, state, count,
		started_ms, version, shown) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		j.Key, string(j.Kind), j.Conv, j.Transport, j.Principal, j.Title, string(j.State), j.Count, j.Started.UnixMilli(),
		c.version, c.shown); err != nil {
		return fmt.Errorf("jobs: insert %s: %w", j.Key, err)
	}
	return nil
}

// save stores what changed of a job and its card.
func (s *Store) save(ctx context.Context, c row) error {
	j := c.job
	if _, err := s.db.ExecContext(ctx, `UPDATE background_jobs SET state = ?, count = ?, started_ms = ?, ended_ms = ?, version = ?, shown = ?
		WHERE key = ?`, string(j.State), j.Count, j.Started.UnixMilli(), unixMilli(j.Ended), c.version, c.shown, j.Key); err != nil {
		return fmt.Errorf("jobs: save %s: %w", j.Key, err)
	}
	return nil
}

// open are the jobs whose rows say they run or wait.
func (s *Store) open(ctx context.Context) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cardColumns+` FROM background_jobs
		WHERE state IN ('queued', 'running') ORDER BY started_ms, key`)
	if err != nil {
		return nil, fmt.Errorf("jobs: open: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: open: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// prune removes the jobs that ended before t.
func (s *Store) prune(ctx context.Context, t time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM background_jobs WHERE state NOT IN ('queued', 'running') AND ended_ms < ?`,
		t.UnixMilli()); err != nil {
		return fmt.Errorf("jobs: prune: %w", err)
	}
	return nil
}

// unixMilli is t in Unix milliseconds, 0 for the zero time (not ended).
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
