// Package language keeps each person's language (R-LANG-1, ADR-0053).
package language

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the people table.
type Store struct {
	db      *sql.DB
	now     func() time.Time
	packsMu sync.RWMutex
	packs   map[texts.Lang]map[texts.Key]pack
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New migrates the "language" component and returns the store.
func New(ctx context.Context, db *sql.DB, opts ...Option) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "language", sub); err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	if err := s.loadPacks(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Seen records the language a person's channel reported. A value outside the table of native names
// counts as missing and never replaces one already known. It writes only when the language changed,
// so a message costs no write.
func (s *Store) Seen(ctx context.Context, principal, code string) error {
	l, ok := texts.Normalize(code)
	if !ok || principal == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO people (principal, seen, updated_ms) VALUES (?, ?, ?)
		 ON CONFLICT (principal) DO UPDATE SET seen = excluded.seen, updated_ms = excluded.updated_ms
		 WHERE seen <> excluded.seen`,
		principal, string(l), s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("language: seen %s: %w", principal, err)
	}
	return nil
}

// Resolve returns a person's language: the one they chose, the last one seen, or English, which is
// also the language of a message with no person behind it.
func (s *Store) Resolve(ctx context.Context, principal string) (texts.Lang, error) {
	var chosen, seen string
	err := s.db.QueryRowContext(ctx, `SELECT chosen, seen FROM people WHERE principal = ?`, principal).Scan(&chosen, &seen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return texts.English, nil
	case err != nil:
		return texts.English, fmt.Errorf("language: resolve %s: %w", principal, err)
	case chosen != "":
		return texts.Lang(chosen), nil
	case seen != "":
		return texts.Lang(seen), nil
	default:
		return texts.English, nil
	}
}

// Chosen returns the language a person chose, if they did.
func (s *Store) Chosen(ctx context.Context, principal string) (texts.Lang, bool, error) {
	var chosen string
	err := s.db.QueryRowContext(ctx, `SELECT chosen FROM people WHERE principal = ?`, principal).Scan(&chosen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("language: chosen %s: %w", principal, err)
	default:
		return texts.Lang(chosen), chosen != "", nil
	}
}

// Choose records the language a person asked for, and reports whether it changed anything.
func (s *Store) Choose(ctx context.Context, principal string, l texts.Lang) (changed bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO people (principal, chosen, updated_ms) VALUES (?, ?, ?)
		 ON CONFLICT (principal) DO UPDATE SET chosen = excluded.chosen, updated_ms = excluded.updated_ms
		 WHERE chosen <> excluded.chosen`,
		principal, string(l), s.now().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("language: choose %s: %w", principal, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("language: choose %s: %w", principal, err)
	}
	return n > 0, nil
}

// NoticeFor reports whether event's message should tell principal that l's texts are a machine's:
// once per language, and again for the same event, since a rerun writes the same message.
func (s *Store) NoticeFor(ctx context.Context, principal string, l texts.Lang, event string) (bool, error) {
	due := false
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var notified string
		err := c.QueryRowContext(ctx, `SELECT notified FROM people WHERE principal = ?`, principal).Scan(&notified)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		entries := map[string]string{}
		if notified != "" {
			for e := range strings.SplitSeq(notified, ",") {
				lang, ev, _ := strings.Cut(e, "=")
				entries[lang] = ev
			}
		}
		if ev, told := entries[string(l)]; told {
			due = ev == event
			return nil
		}
		due = true
		entries[string(l)] = event
		parts := make([]string, 0, len(entries))
		for _, lang := range slices.Sorted(maps.Keys(entries)) {
			parts = append(parts, lang+"="+entries[lang])
		}
		_, err = c.ExecContext(ctx, `INSERT INTO people (principal, notified, updated_ms) VALUES (?, ?, ?)
			ON CONFLICT (principal) DO UPDATE SET notified = excluded.notified`,
			principal, strings.Join(parts, ","), s.now().UnixMilli())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("language: notice %s: %w", principal, err)
	}
	return due, nil
}
