package modstore

import (
	"context"
	"fmt"
	"time"
)

// Entry is one version in a module's journal (SHA256 is the build's Digest, wasm and manifest): who asked for it, in whose
// words, from which commit, and how it has run.
type Entry struct {
	Version, SHA256, State, By, Note, Commit string
	Installed                                time.Time
	Runs, Failures, Exceptions               int
	LastFailureKind                          string
	LastFailure                              time.Time // zero when it never failed
}

// CallResult is how one call of a module ended.
type CallResult struct {
	Failed    bool   // the call ended in a failure: an error, a refused answer
	Exception bool   // the module itself broke: a trap or a timeout
	Kind      string // the failure's kind, as the node names it (module.format…)
}

// Journal is every version of a module, newest first, the retired ones too.
func (s *Store) Journal(ctx context.Context, module string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, sha256, state, "by", note, commit_id, installed_ms,
			runs, failures, exceptions, last_failure_kind, last_failure_ms
		FROM module_versions WHERE module = ? ORDER BY installed_ms DESC, rowid DESC`, module)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			e              Entry
			installed, fms int64
		)
		if err := rows.Scan(&e.Version, &e.SHA256, &e.State, &e.By, &e.Note, &e.Commit, &installed,
			&e.Runs, &e.Failures, &e.Exceptions, &e.LastFailureKind, &fms); err != nil {
			return nil, fmt.Errorf("modstore: %w", err)
		}
		e.Installed = time.UnixMilli(installed).UTC()
		if fms != 0 {
			e.LastFailure = time.UnixMilli(fms).UTC()
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	return out, nil
}

// Count adds one run of the version to its journal row, and a failure or an
// exception when the call had one. A database that fails is logged: counting
// must never fail a call.
func (s *Store) Count(ctx context.Context, module, sha string, r CallResult) {
	failed, exc := 0, 0
	if r.Failed || r.Exception {
		failed = 1
	}
	if r.Exception {
		exc = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE module_versions SET runs = runs + 1, failures = failures + ?, exceptions = exceptions + ?,
			last_failure_kind = CASE WHEN ? = 1 THEN ? ELSE last_failure_kind END,
			last_failure_ms = CASE WHEN ? = 1 THEN ? ELSE last_failure_ms END
		WHERE module = ? AND sha256 = ?`,
		failed, exc, failed, r.Kind, failed, s.now().UnixMilli(), module, sha)
	if err != nil {
		s.log.Warn("modstore: counting a run failed", "module", module, "err", err)
	}
}
