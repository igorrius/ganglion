package modstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// notifyDay is the day key of the notify count: day's date as day's own
// location has it, so the caller's DayStart in the node's zone decides
// where a day ends (R-RUN-16).
func notifyDay(day time.Time) string { return day.Format(time.DateOnly) }

// CountNotify adds one to the module's count for day and returns the new count and whether
// the limit line was already sent that day. day is the start of the day in the node's time
// zone (clock.DayStart); a message is counted before it is put (ruling 11 of M4c).
func (s *Store) CountNotify(ctx context.Context, module string, day time.Time) (n int, limited bool, err error) {
	err = s.db.QueryRowContext(ctx, `INSERT INTO notify_days (module, day, sent) VALUES (?, ?, 1)
		ON CONFLICT (module, day) DO UPDATE SET sent = notify_days.sent + 1
		RETURNING sent, limited`, module, notifyDay(day)).Scan(&n, &limited)
	if err != nil {
		return 0, false, fmt.Errorf("modstore: count notify: %w", err)
	}
	return n, limited, nil
}

// MarkLimited notes that the owner was told the module reached its limit on day, so the
// line goes once a day. Marking a day twice is a no-op.
func (s *Store) MarkLimited(ctx context.Context, module string, day time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO notify_days (module, day, limited) VALUES (?, ?, 1)
		ON CONFLICT (module, day) DO UPDATE SET limited = 1`, module, notifyDay(day)); err != nil {
		return fmt.Errorf("modstore: mark limited: %w", err)
	}
	return nil
}

// NotifiedOn is how many messages the module counted on day, for its card; 0 for a day
// with none.
func (s *Store) NotifiedOn(ctx context.Context, module string, day time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT sent FROM notify_days WHERE module = ? AND day = ?`, module, notifyDay(day)).Scan(&n)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("modstore: notified on: %w", err)
	}
	return n, nil
}
