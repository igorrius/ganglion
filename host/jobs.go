package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
)

// interruptedText is what the model reads of a command the node stopped by
// restarting: a change may or may not have happened, so it is not run again.
const interruptedText = "interrupted: the node restarted while this command ran, so it was stopped; " +
	"whether it finished is not known. Check its effect before running it again"

// jobColumns are a row's columns in the order scanJob reads them.
const jobColumns = `key, conv, transport, principal, depth, argv, dir, state, background, text, started_ms, ended_ms`

// rowScanner is a *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanJob reads one row of jobColumns.
func scanJob(s rowScanner) (Job, error) {
	var (
		j              Job
		argv, state    string
		started, ended int64
	)
	if err := s.Scan(&j.Key, &j.Conv, &j.Transport, &j.Principal, &j.Depth, &argv, &j.Dir, &state,
		&j.Background, &j.Text, &started, &ended); err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal([]byte(argv), &j.Argv); err != nil {
		return Job{}, fmt.Errorf("host: job %s: argv: %w", j.Key, err)
	}
	j.State, j.Started = JobState(state), time.UnixMilli(started).UTC()
	if ended != 0 {
		j.Ended = time.UnixMilli(ended).UTC()
	}
	return j, nil
}

// begin stores the request as running; fresh is false when its key has a
// row already, which it returns instead.
func (r *Runner) begin(ctx context.Context, q Request) (j Job, fresh bool, err error) {
	argv, err := json.Marshal(q.Argv)
	if err != nil {
		return Job{}, false, fmt.Errorf("host: begin %s: %w", q.Key, err)
	}
	now := r.now()
	res, err := r.db.ExecContext(ctx, `INSERT INTO host_jobs (key, conv, transport, principal, depth, argv, dir, state, started_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?) ON CONFLICT (key) DO NOTHING`,
		q.Key, q.Conv, q.Transport, q.Principal, q.Depth, string(argv), q.Dir, now.UnixMilli())
	if err != nil {
		return Job{}, false, fmt.Errorf("host: begin %s: %w", q.Key, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		j, err := r.Get(ctx, q.Key)
		return j, false, err
	}
	return Job{Key: q.Key, Conv: q.Conv, Transport: q.Transport, Principal: q.Principal, Depth: q.Depth,
		Argv: q.Argv, Dir: q.Dir, State: JobRunning, Started: time.UnixMilli(now.UnixMilli()).UTC()}, true, nil
}

// end stores how a running command ended and returns its row.
func (r *Runner) end(ctx context.Context, key string, state JobState, text string) (Job, error) {
	j, err := scanJob(r.db.QueryRowContext(ctx, `UPDATE host_jobs SET state = ?, text = ?, ended_ms = ?
		WHERE key = ? AND state = 'running' RETURNING `+jobColumns, string(state), text, r.now().UnixMilli(), key))
	if err != nil {
		return Job{}, fmt.Errorf("host: end %s: %w", key, err)
	}
	return j, nil
}

// Get returns the row of key, or ErrNoJob.
func (r *Runner) Get(ctx context.Context, key string) (Job, error) {
	j, err := scanJob(r.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM host_jobs WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("host: get %s: %w", key, err)
	}
	return j, nil
}

// Running returns the keys among keys whose command still runs.
func (r *Runner) Running(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := r.db.QueryContext(ctx, `SELECT key FROM host_jobs WHERE state = 'running' AND key IN (?`+
		strings.Repeat(", ?", len(keys)-1)+`) ORDER BY key`, args...)
	if err != nil {
		return nil, fmt.Errorf("host: running: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("host: running: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// interrupt marks the commands a stopped node left running as interrupted.
// A background one stays background and unannounced, so the next pass
// announces it.
func (r *Runner) interrupt(ctx context.Context) error {
	res, err := r.db.ExecContext(ctx, `UPDATE host_jobs SET state = 'interrupted', text = ?, ended_ms = ? WHERE state = 'running'`,
		interruptedText, r.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("host: interrupt: %w", err)
	}
	n, _ := res.RowsAffected()
	r.tel.interrupted(ctx, n)
	return nil
}

// pass stores the command.done event of each background command that
// ended and is not announced yet, then prunes old rows. A failed put is
// logged and tried again on the next pass.
func (r *Runner) pass(ctx context.Context) {
	jobs, err := r.unannounced(ctx)
	if err != nil {
		r.logErr(ctx, "host: the ended background commands were not read", err)
	}
	for _, j := range jobs {
		r.announce(ctx, j)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM host_jobs WHERE state != 'running' AND (background = 0 OR announced = 1)
		AND ended_ms < ?`, r.now().Add(-keepJobs).UnixMilli()); err != nil {
		r.logErr(ctx, "host: old commands were not pruned", err)
	}
}

// unannounced are the background commands that ended and have no event.
func (r *Runner) unannounced(ctx context.Context) ([]Job, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM host_jobs
		WHERE state != 'running' AND background = 1 AND announced = 0 ORDER BY ended_ms, key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// announce puts j's event and marks it announced.
func (r *Runner) announce(ctx context.Context, j Job) {
	if err := r.put(ctx, doneEvent(j, r.now())); err != nil {
		r.logErr(ctx, "host: the end of a background command was not announced", err, "key", j.Key)
		return
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE host_jobs SET announced = 1 WHERE key = ?`, j.Key); err != nil {
		// The event is stored once by its key, so the next pass's put is a no-op.
		r.logErr(ctx, "host: a command's announcement was not marked", err, "key", j.Key)
	}
}

// logErr logs err unless the node is stopping.
func (r *Runner) logErr(ctx context.Context, msg string, err error, args ...any) {
	if ctx.Err() != nil {
		return
	}
	r.log.Error(msg, append(args, "err", err)...)
}

// doneEvent resumes the conversation of a command that ended in the
// background. Its Source is the conversation's transport, so the answer goes
// back there; its key is fixed per command, so it is stored once.
func doneEvent(j Job, now time.Time) event.Event {
	payload, err := json.Marshal(event.CommandDone{Key: j.Key})
	if err != nil {
		panic("host: a struct of strings always marshals: " + err.Error())
	}
	return event.Event{ID: event.NewID(now), IdemKey: "command:" + j.Key + ":done", Source: j.Transport, Conv: j.Conv,
		Kind: event.KindCommandDone, Payload: payload, At: now}
}
