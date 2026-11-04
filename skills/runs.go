package skills

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// runsRetain is how long a run is remembered: far longer than any event can
// come back, as long as the tool journal keeps its rows.
const runsRetain = 30 * 24 * time.Hour

// Runs records which change scripts have started, so a crash cannot run one
// twice for one call (R-DLV-6).
type Runs struct {
	db  *sql.DB
	now func() time.Time
}

// OpenRuns migrates the "skill_runs" component.
func OpenRuns(ctx context.Context, db *sql.DB, now func() time.Time) (*Runs, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "skill_runs", sub); err != nil {
		return nil, err
	}
	return &Runs{db: db, now: now}, nil
}

// Begin records that key is about to run, committed before it returns. It is
// false when key has begun before. Old rows go in the same call.
func (r *Runs) Begin(ctx context.Context, key, skill string) (bool, error) {
	now := r.now()
	if _, err := r.db.ExecContext(ctx, `DELETE FROM skill_runs WHERE started_ms < ?`, now.Add(-runsRetain).UnixMilli()); err != nil {
		return false, fmt.Errorf("skills: runs: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO skill_runs (key, skill, started_ms) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		key, skill, now.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("skills: runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("skills: runs: %w", err)
	}
	return n == 1, nil
}
