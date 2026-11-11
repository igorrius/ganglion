package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// Reviews arms the memory review of a conversation (ReviewDue); NoReviews
// does nothing, for a node with the review off.
type Reviews interface {
	// Arm sets when conv's session is due for a review, replacing an
	// earlier time: "" is the live session, else an archived one's id.
	Arm(ctx context.Context, conv, session, transport, principal string, due time.Time) error
}

// NoReviews is the Reviews of a node with the memory review off.
type NoReviews struct{}

// Arm implements Reviews.
func (NoReviews) Arm(context.Context, string, string, string, string, time.Time) error { return nil }

// ReviewDue is the review_due table: when each conversation's review is
// due (R-MEM-8, ADR-0072).
type ReviewDue struct{ db *sql.DB }

// NewReviewDue returns the table in db, creating it when it is missing.
func NewReviewDue(ctx context.Context, db *sql.DB) (*ReviewDue, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "sessions", sub); err != nil {
		return nil, fmt.Errorf("agent: review_due: %w", err)
	}
	return &ReviewDue{db: db}, nil
}

// Arm implements Reviews: an upsert, so a rerun turn is harmless.
func (d *ReviewDue) Arm(ctx context.Context, conv, session, transport, principal string, due time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO review_due (conv, session, transport, principal, due_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (conv, session) DO UPDATE SET transport = excluded.transport, principal = excluded.principal, due_ms = excluded.due_ms`,
		conv, session, transport, principal, due.UnixMilli())
	if err != nil {
		return fmt.Errorf("agent: arm the review of %s: %w", conv, err)
	}
	return nil
}

// dueRow is one due review.
type dueRow struct {
	Conv, Session, Transport, Principal string
	Due                                 time.Time
}

// due lists the rows due at now, oldest first.
func (d *ReviewDue) due(ctx context.Context, now time.Time) ([]dueRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT conv, session, transport, principal, due_ms FROM review_due WHERE due_ms <= ?
		ORDER BY due_ms, conv, session`, now.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("agent: due reviews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []dueRow
	for rows.Next() {
		var r dueRow
		var ms int64
		if err := rows.Scan(&r.Conv, &r.Session, &r.Transport, &r.Principal, &ms); err != nil {
			return nil, fmt.Errorf("agent: due reviews: %w", err)
		}
		r.Due = time.UnixMilli(ms).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// done removes r unless it was armed again for another time meanwhile.
func (d *ReviewDue) done(ctx context.Context, r dueRow) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM review_due WHERE conv = ? AND session = ? AND due_ms = ?`,
		r.Conv, r.Session, r.Due.UnixMilli())
	if err != nil {
		return fmt.Errorf("agent: review of %s done: %w", r.Conv, err)
	}
	return nil
}

// PutFunc puts an event in the inbox (sqlqueue.Queue.Put).
type PutFunc func(ctx context.Context, e event.Event) error

// ReviewTimer turns due rows of review_due into memory.review events
// (R-MEM-8): a component of the core. An event's key holds the
// conversation, the session and the due time, so a crash between the put
// and the removal puts it once.
type ReviewTimer struct {
	due   *ReviewDue
	put   PutFunc
	now   func() time.Time
	every time.Duration
	log   *slog.Logger
}

// NewReviewTimer returns the timer over due, putting events with put and
// looking every every.
func NewReviewTimer(due *ReviewDue, put PutFunc, now func() time.Time, every time.Duration, log *slog.Logger) *ReviewTimer {
	return &ReviewTimer{due: due, put: put, now: now, every: every, log: log}
}

// Name implements node.Component.
func (*ReviewTimer) Name() string { return "memory-review" }

// Run ticks every t.every until ctx ends. A failed tick is logged and
// tried again at the next.
func (t *ReviewTimer) Run(ctx context.Context) error {
	tick := time.NewTicker(t.every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := t.Tick(ctx); err != nil && ctx.Err() == nil {
				t.log.Warn("agent: memory review timer", "err", err)
			}
		}
	}
}

// Tick puts one event for every row due now and removes the row. The
// first failure stops the tick; its row stays for the next.
func (t *ReviewTimer) Tick(ctx context.Context) error {
	now := t.now()
	rows, err := t.due.due(ctx, now)
	if err != nil {
		return err
	}
	for _, r := range rows {
		payload, err := json.Marshal(event.MemoryReview{Principal: r.Principal, Session: r.Session})
		if err != nil {
			return err
		}
		key := "memory.review:" + r.Conv + ":" + r.Session + ":" + strconv.FormatInt(r.Due.UnixMilli(), 10)
		e := event.Event{ID: event.NewID(now), IdemKey: key, Source: r.Transport, Conv: r.Conv, Kind: event.KindMemoryReview,
			Payload: payload, At: now}
		if err := t.put(ctx, e); err != nil {
			return fmt.Errorf("agent: put the review of %s: %w", r.Conv, err)
		}
		if err := t.due.done(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// ReviewOff is the /status line of a node whose memory review is switched
// off in its config while memory is on (R-MEM-8, R-OBS-11).
type ReviewOff struct{}

// Name implements status.Source.
func (ReviewOff) Name() string { return "memory-review" }

// Status implements status.Source.
func (ReviewOff) Status(context.Context, status.Scope) ([]status.Check, error) {
	return []status.Check{{Section: status.SectionProblems, State: status.Off, Line: texts.StatusMemoryReviewOff}}, nil
}
