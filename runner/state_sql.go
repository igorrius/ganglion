package runner

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// stateTimeout bounds one state query: a module call is short, so a
// database that does not answer fails the call's read, not the node.
const stateTimeout = 5 * time.Second

// SQLState is a State kept in the module_state table of the node's
// database (created by the modstore migrations), so module state survives
// restarts and version swaps.
type SQLState struct {
	db  *sql.DB
	log *slog.Logger
}

// SQLStateOption configures a SQLState.
type SQLStateOption func(*SQLState)

// WithStateLogger sets where failed reads and writes are logged; the
// default discards them.
func WithStateLogger(l *slog.Logger) SQLStateOption {
	return func(s *SQLState) { s.log = l }
}

// NewSQLState returns a State over db.
func NewSQLState(db *sql.DB, opts ...SQLStateOption) State {
	s := &SQLState{db: db, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get returns a module's value. A failed read is reported as absent.
func (s *SQLState) Get(module, key string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), stateTimeout)
	defer cancel()
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM module_state WHERE module = ? AND key = ?`, module, key).Scan(&v)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("module state read failed", "module", module, "err", err)
		}
		return "", false
	}
	return v, true
}

// Put sets a module's value. A failed write is logged: the State interface
// has no error, and the module's call goes on.
func (s *SQLState) Put(module, key, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), stateTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO module_state (module, key, value) VALUES (?, ?, ?)
		ON CONFLICT (module, key) DO UPDATE SET value = excluded.value`, module, key, value)
	if err != nil {
		s.log.Warn("module state write failed", "module", module, "err", err)
	}
}
