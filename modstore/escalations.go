package modstore

import (
	"context"
	"fmt"
	"time"
)

// OpenEpisode opens the escalation episode of kind for module's version sha
// and reports whether it is new: false when an episode of that kind is open
// for the same version already, so the caller does not escalate again. An
// episode of an older version is replaced, for a new version starts afresh.
// at is when the episode opened; the caller keys its event by it.
func (s *Store) OpenEpisode(ctx context.Context, module, kind, sha string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO module_escalations (module, kind, sha, opened_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT (module, kind) DO UPDATE SET sha = excluded.sha, opened_ms = excluded.opened_ms
		WHERE module_escalations.sha != excluded.sha`, module, kind, sha, at.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("modstore: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("modstore: %w", err)
	}
	return n > 0, nil
}

// CloseEpisode forgets the open episode of kind for module, so the next
// failure of that kind escalates again; the caller uses it to take back an
// episode whose event was not put.
func (s *Store) CloseEpisode(ctx context.Context, module, kind string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM module_escalations WHERE module = ? AND kind = ?`, module, kind); err != nil {
		return fmt.Errorf("modstore: %w", err)
	}
	return nil
}

// ClearEpisodes closes every open episode of module: it recovered, or a new
// version, a rollback or an "on" gave it a fresh start.
func (s *Store) ClearEpisodes(ctx context.Context, module string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM module_escalations WHERE module = ?`, module); err != nil {
		return fmt.Errorf("modstore: %w", err)
	}
	return nil
}

// HasEpisodes reports whether module has an episode open.
func (s *Store) HasEpisodes(ctx context.Context, module string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM module_escalations WHERE module = ?`, module).Scan(&n); err != nil {
		return false, fmt.Errorf("modstore: %w", err)
	}
	return n > 0, nil
}
