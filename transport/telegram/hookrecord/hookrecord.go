// Package hookrecord remembers in the node's database that this node set
// a Telegram webhook (R-DLV-18, ADR-0078). A node set back to polling
// removes the webhook it recorded and never one somebody else set.
package hookrecord

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

// Store is the record; it implements telegram.WebhookRecord.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New migrates the "telegram" component and returns the record.
func New(ctx context.Context, db *sql.DB, now func() time.Time) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "telegram", sub); err != nil {
		return nil, err
	}
	return &Store{db: db, now: now}, nil
}

// URL is the webhook this node set; "" when it set none.
func (s *Store) URL(ctx context.Context) (string, error) {
	var url string
	err := s.db.QueryRowContext(ctx, `SELECT url FROM telegram_webhook WHERE id = 1`).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hookrecord: read: %w", err)
	}
	return url, nil
}

// Set records that this node set the webhook url.
func (s *Store) Set(ctx context.Context, url string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO telegram_webhook (id, url, set_ms) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET url = excluded.url, set_ms = excluded.set_ms`, url, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("hookrecord: set: %w", err)
	}
	return nil
}

// Clear forgets the webhook: the node removed it.
func (s *Store) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM telegram_webhook`); err != nil {
		return fmt.Errorf("hookrecord: clear: %w", err)
	}
	return nil
}
