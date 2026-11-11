package agent

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// SessionStore keeps the ended sessions of every conversation (R-AGT-6).
type SessionStore interface {
	// Put archives s, ended at s.Last, by endedBy (idle, new, resume).
	Put(ctx context.Context, conv string, s State, endedBy string) error
	// List returns the last n archived sessions of conv, newest first,
	// leaving out the session except.
	List(ctx context.Context, conv string, n int, except string) ([]SessionInfo, error)
	// Get returns an archived session.
	Get(ctx context.Context, conv, session string) (State, error)
	// Search finds archived sessions of convs by text, for recall
	// (memory.Episodes).
	Search(ctx context.Context, q string, convs []string, limit int) ([]memory.Episode, error)
	// MarkReviewed records that the memory review read the first n messages
	// of an archived session (R-MEM-8), so a resumed session is not read again.
	MarkReviewed(ctx context.Context, conv, session string, n int) error
}

// NoArchive is a SessionStore that keeps nothing: ended sessions are
// dropped and /sessions finds none.
type NoArchive struct{}

// Put implements SessionStore.
func (NoArchive) Put(context.Context, string, State, string) error { return nil }

// List implements SessionStore.
func (NoArchive) List(context.Context, string, int, string) ([]SessionInfo, error) { return nil, nil }

// Search implements SessionStore: there is nothing to find.
func (NoArchive) Search(context.Context, string, []string, int) ([]memory.Episode, error) {
	return nil, nil
}

// MarkReviewed implements SessionStore.
func (NoArchive) MarkReviewed(context.Context, string, string, int) error { return nil }

// Get implements SessionStore.
func (NoArchive) Get(_ context.Context, _, session string) (State, error) {
	return State{}, fmt.Errorf("agent: get session %s: no archive", session)
}

// Archive is the SQLite SessionStore.
type Archive struct{ db *sql.DB }

var _ SessionStore = (*Archive)(nil)

// SessionInfo is one line of /sessions.
type SessionInfo struct {
	Session        string
	Started, Ended time.Time
	Turns          int
	Title          string // the first user message, at most 60 runes
}

// NewArchive migrates the "sessions" component.
func NewArchive(ctx context.Context, db *sql.DB) (*Archive, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "sessions", sub); err != nil {
		return nil, err
	}
	return &Archive{db: db}, nil
}

func title(s State) string {
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			return text.Truncate(m.Content, 60)
		}
	}
	return ""
}

// Put archives s, ended at s.Last. It is keyed by (conv, session), so a
// turn that reruns after a crash writes the same row again.
func (a *Archive) Put(ctx context.Context, conv string, s State, endedBy string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	turns := 0
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			turns++
		}
	}
	_, err = a.db.ExecContext(ctx, `INSERT OR REPLACE INTO sessions
		(conv, session, started_ms, ended_ms, ended_by, turns, title, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		conv, s.Session, ms(s.Started), ms(s.Last), endedBy, turns, title(s), string(data))
	if err != nil {
		return fmt.Errorf("agent: archive session %s: %w", s.Session, err)
	}
	return nil
}

// List returns the last n archived sessions of conv, newest first,
// leaving out the session except (the current one: it may sit in the
// archive as an older copy after a /resume).
func (a *Archive) List(ctx context.Context, conv string, n int, except string) ([]SessionInfo, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT session, started_ms, ended_ms, turns, title FROM sessions
		WHERE conv = ? AND session != ? ORDER BY ended_ms DESC, rowid DESC LIMIT ?`, conv, except, n)
	if err != nil {
		return nil, fmt.Errorf("agent: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		var started, ended int64
		if err := rows.Scan(&s.Session, &started, &ended, &s.Turns, &s.Title); err != nil {
			return nil, err
		}
		s.Started, s.Ended = fromMS(started), fromMS(ended)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get returns an archived session.
func (a *Archive) Get(ctx context.Context, conv, session string) (State, error) {
	var data string
	if err := a.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE conv = ? AND session = ?`, conv, session).Scan(&data); err != nil {
		return State{}, fmt.Errorf("agent: get session %s: %w", session, err)
	}
	return DecodeState(json.RawMessage(data))
}

// MarkReviewed implements SessionStore: the stored state's mark, and only
// that, changes.
func (a *Archive) MarkReviewed(ctx context.Context, conv, session string, n int) error {
	if _, err := a.db.ExecContext(ctx, `UPDATE sessions SET data = json_set(data, '$.reviewed', ?) WHERE conv = ? AND session = ?`,
		n, conv, session); err != nil {
		return fmt.Errorf("agent: mark session %s reviewed: %w", session, err)
	}
	return nil
}
