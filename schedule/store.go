package schedule

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store keeps the jobs and their runs in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore migrates the "schedule" component.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "schedule", sub); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

const jobColumns = `id, owner, conv, transport, kind, cron, at_ms, text, grant_json, system, created_ms, deleted, deleted_by`

// Create stores a new job. ErrInvalid for a job that is not complete or
// whose cron does not parse, ErrExists for a taken id.
func (s *Store) Create(ctx context.Context, j Job) error {
	inserted, err := s.insert(ctx, j)
	if err != nil {
		return err
	}
	if !inserted {
		return fmt.Errorf("schedule: create %s: %w", j.ID, ErrExists)
	}
	return nil
}

// Ensure stores j unless a job with its id exists; an existing job is
// never changed. The node ensures its system jobs this way at every
// start.
func (s *Store) Ensure(ctx context.Context, j Job) error {
	_, err := s.insert(ctx, j)
	return err
}

// EnsureSystem stores system job j (Kind KindSystem, System set), or
// replaces the stored system job of its id with it: the node owns its
// system jobs, so the current release's shape and the current first
// admin win at every start (ruling R36). ErrInvalid for any other job,
// ErrExists when the id belongs to a job that is not a system job.
func (s *Store) EnsureSystem(ctx context.Context, j Job) error {
	if j.Kind != KindSystem || !j.System {
		return fmt.Errorf("%w %s: not a system job", ErrInvalid, j.ID)
	}
	if err := j.validate(); err != nil {
		return err
	}
	at := int64(0)
	if !j.At.IsZero() {
		at = j.At.UnixMilli()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO schedule_jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', 1, ?, 0, '')
		ON CONFLICT (id) DO UPDATE SET owner = excluded.owner, conv = excluded.conv, transport = excluded.transport,
		kind = excluded.kind, cron = excluded.cron, at_ms = excluded.at_ms, text = excluded.text, grant_json = '', deleted = 0, deleted_by = ''
		WHERE schedule_jobs.system = 1`, j.ID, j.Owner, j.Conv, j.Transport, string(j.Kind), j.Cron, at, j.Text, j.Created.UnixMilli())
	if err != nil {
		return fmt.Errorf("schedule: ensure %s: %w", j.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("schedule: ensure %s: %w", j.ID, ErrExists)
	}
	return nil
}

// insert stores j; false when its id is taken.
func (s *Store) insert(ctx context.Context, j Job) (bool, error) {
	if err := j.validate(); err != nil {
		return false, err
	}
	grant := ""
	if j.Kind == KindTask {
		b, err := json.Marshal(j.Grant)
		if err != nil {
			return false, fmt.Errorf("schedule: store %s: %w", j.ID, err)
		}
		grant = string(b)
	}
	at := int64(0)
	if !j.At.IsZero() {
		at = j.At.UnixMilli()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO schedule_jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '')
		ON CONFLICT (id) DO NOTHING`, j.ID, j.Owner, j.Conv, j.Transport, string(j.Kind), j.Cron, at, j.Text, grant, j.System,
		j.Created.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("schedule: store %s: %w", j.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("schedule: store %s: %w", j.ID, err)
	}
	return n > 0, nil
}

// Get returns job id, deleted or not; ErrNotFound when there is none.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM schedule_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("schedule: job %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Job{}, fmt.Errorf("schedule: job %s: %w", id, err)
	}
	return j, nil
}

// List returns owner's jobs that can still run, oldest first: not
// deleted, and not a one-time job that has run.
func (s *Store) List(ctx context.Context, owner string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM schedule_jobs j WHERE owner = ? AND deleted = 0
		AND NOT (cron = '' AND EXISTS (SELECT 1 FROM cron_runs r WHERE r.job = j.id)) ORDER BY created_ms, id`, owner)
	if err != nil {
		return nil, fmt.Errorf("schedule: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("schedule: list: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("schedule: list: %w", err)
	}
	return out, nil
}

// Delete marks owner's job id deleted, so it never fires again, and
// records by, the token of the press or call that deleted it
// (Job.DeletedBy); deleting it twice is not an error and keeps the first
// token. ErrNotFound for a job that is not owner's, ErrSystem for a
// system job.
func (s *Store) Delete(ctx context.Context, id, owner, by string) error {
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var jobOwner string
		var system, deleted bool
		err := c.QueryRowContext(ctx, `SELECT owner, system, deleted FROM schedule_jobs WHERE id = ?`, id).Scan(&jobOwner, &system, &deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows) || err == nil && jobOwner != owner:
			return ErrNotFound
		case err != nil:
			return err
		case system:
			return ErrSystem
		case deleted:
			return nil
		}
		_, err = c.ExecContext(ctx, `UPDATE schedule_jobs SET deleted = 1, deleted_by = ? WHERE id = ?`, by, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("schedule: delete %s: %w", id, err)
	}
	return nil
}

// activeJob is a job that is not deleted, with the slot of its last
// scheduled run (zero for none).
type activeJob struct {
	Job
	last time.Time
}

// active returns the jobs that are not deleted, with their last runs.
func (s *Store) active(ctx context.Context) ([]activeJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+`,
		COALESCE((SELECT MAX(scheduled_ms) FROM cron_runs r WHERE r.job = j.id), 0)
		FROM schedule_jobs j WHERE deleted = 0 ORDER BY created_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("schedule: due jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []activeJob
	for rows.Next() {
		var a activeJob
		var last int64
		if a.Job, err = scanJob(rows, &last); err != nil {
			return nil, fmt.Errorf("schedule: due jobs: %w", err)
		}
		if last != 0 {
			a.last = time.UnixMilli(last).UTC()
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// claim records the run of job id for slot, in one transaction with the
// check that the job is not deleted. false when the job is deleted or
// the run is recorded already.
func (s *Store) claim(ctx context.Context, id string, slot time.Time) (bool, error) {
	claimed := false
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var deleted bool
		if err := c.QueryRowContext(ctx, `SELECT deleted FROM schedule_jobs WHERE id = ?`, id).Scan(&deleted); err != nil || deleted {
			return err
		}
		res, err := c.ExecContext(ctx, `INSERT INTO cron_runs (job, scheduled_ms, put_ms) VALUES (?, ?, 0)
			ON CONFLICT (job, scheduled_ms) DO NOTHING`, id, slot.UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n > 0
		return err
	})
	if err != nil {
		return false, fmt.Errorf("schedule: record the run of %s: %w", id, err)
	}
	return claimed, nil
}

// unputRun is a recorded run whose event may not be in the inbox.
type unputRun struct {
	job  Job
	slot time.Time
}

// unput returns the recorded runs of jobs that are not deleted whose
// events were not put.
func (s *Store) unput(ctx context.Context) ([]unputRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("j.", jobColumns)+`, r.scheduled_ms FROM cron_runs r
		JOIN schedule_jobs j ON j.id = r.job WHERE r.put_ms = 0 AND j.deleted = 0 ORDER BY r.scheduled_ms`)
	if err != nil {
		return nil, fmt.Errorf("schedule: runs not put: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []unputRun
	for rows.Next() {
		var r unputRun
		var slot int64
		if r.job, err = scanJob(rows, &slot); err != nil {
			return nil, fmt.Errorf("schedule: runs not put: %w", err)
		}
		r.slot = time.UnixMilli(slot).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// markPut records that the event of a scheduled run is in the inbox.
func (s *Store) markPut(ctx context.Context, id string, slot, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE cron_runs SET put_ms = ? WHERE job = ? AND scheduled_ms = ? AND put_ms = 0`,
		now.UnixMilli(), id, slot.UnixMilli()); err != nil {
		return fmt.Errorf("schedule: mark the run of %s put: %w", id, err)
	}
	return nil
}

// prefixed puts prefix before each of the comma-separated columns.
func prefixed(prefix, columns string) string {
	return prefix + strings.ReplaceAll(columns, ", ", ", "+prefix)
}

type scanner interface{ Scan(dest ...any) error }

// scanJob reads the job columns and then extra.
func scanJob(r scanner, extra ...any) (Job, error) {
	var j Job
	var kind, grant string
	var at, created int64
	dest := append([]any{&j.ID, &j.Owner, &j.Conv, &j.Transport, &kind, &j.Cron, &at, &j.Text, &grant, &j.System, &created, &j.Deleted, &j.DeletedBy}, extra...)
	if err := r.Scan(dest...); err != nil {
		return Job{}, err
	}
	if grant != "" {
		if err := json.Unmarshal([]byte(grant), &j.Grant); err != nil {
			return Job{}, fmt.Errorf("grant of %s: %w", j.ID, err)
		}
	}
	if at != 0 {
		j.At = time.UnixMilli(at).UTC()
	}
	j.Kind, j.Created = Kind(kind), time.UnixMilli(created).UTC()
	return j, nil
}

// isBlank reports whether s has only white space.
func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
