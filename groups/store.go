package groups

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

// Status is where a group's request is. Statuses are stored.
type Status string

// Statuses.
const (
	StatusPending Status = "pending" // the admin has the card
	StatusRefused Status = "refused" // the admin said no: the group is not asked about again
)

// Store keeps the requests of groups that are not connected.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New migrates the "groups" component and returns the store.
func New(ctx context.Context, db *sql.DB, now func() time.Time) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "groups", sub); err != nil {
		return nil, err
	}
	return &Store{db: db, now: now}, nil
}

// alive is the oldest asked_ms a pending request may have.
func (s *Store) alive() int64 { return s.now().Add(-RequestTTL).UnixMilli() }

// Status returns the request of group and whether there is one. A pending
// request older than RequestTTL is not one.
func (s *Store) Status(ctx context.Context, group string) (Status, bool, error) {
	var st string
	var asked int64
	err := s.db.QueryRowContext(ctx, `SELECT status, asked_ms FROM group_requests WHERE grp = ?`, group).Scan(&st, &asked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("groups: status of %s: %w", group, err)
	}
	if Status(st) == StatusPending && asked < s.alive() {
		return "", false, nil
	}
	return Status(st), true, nil
}

// Set stores the request of group with st, replacing an older one.
func (s *Store) Set(ctx context.Context, group, title string, st Status) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO group_requests (grp, title, status, asked_ms) VALUES (?, ?, ?, ?)
		 ON CONFLICT (grp) DO UPDATE SET title = excluded.title, status = excluded.status, asked_ms = excluded.asked_ms`,
		group, title, string(st), s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("groups: set %s: %w", group, err)
	}
	return nil
}

// Forget drops the request of group; a group with none is not an error.
func (s *Store) Forget(ctx context.Context, group string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM group_requests WHERE grp = ?`, group); err != nil {
		return fmt.Errorf("groups: forget %s: %w", group, err)
	}
	return nil
}

// Pending counts the requests that wait for an admin now.
func (s *Store) Pending(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_requests WHERE status = ? AND asked_ms >= ?`,
		string(StatusPending), s.alive()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("groups: pending: %w", err)
	}
	return n, nil
}
