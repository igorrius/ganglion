// Package sqlqueue is the SQLite inbox, outbox and conversation state of a
// node (docs/spec.md, 7.1). Open the database with sqlitedb.Open; the queue
// shares the file with the other components.
package sqlqueue

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/sqlitedb"
	"go.opentelemetry.io/otel/metric"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Retention is how long Prune callers keep finished rows. Telegram holds
// unconfirmed updates for 24 hours, so an IdemKey pruned after 7 days
// cannot come back from it.
const Retention = 7 * 24 * time.Hour

// RefRetention is how long Prune callers keep the refs of delivered messages
// with the copy of what a reply to one needs (R-DLV-13, ADR-0055): a reply
// reaches a message up to 90 days old, though the outbox keeps the message
// itself only for Retention.
const RefRetention = 90 * 24 * time.Hour

// Queue is a queue.Queue backed by SQLite.
type Queue struct {
	db          *sql.DB
	now         func() time.Time
	inboxReady  chan struct{}
	outboxReady chan struct{}
	taken       metric.Int64Counter
}

var _ queue.Queue = (*Queue)(nil)

// Option configures a Queue.
type Option func(*Queue)

// WithClock sets the clock used for every timestamp; the default is
// time.Now.
func WithClock(now func() time.Time) Option {
	return func(q *Queue) { q.now = now }
}

// New migrates the "queue" component and returns the queue.
func New(ctx context.Context, db *sql.DB, opts ...Option) (*Queue, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "queue", sub); err != nil {
		return nil, err
	}
	if err := observeDepths(db); err != nil {
		return nil, err
	}
	taken, err := takenCounter()
	if err != nil {
		return nil, err
	}
	q := &Queue{
		db:          db,
		taken:       taken,
		now:         time.Now,
		inboxReady:  make(chan struct{}, 1),
		outboxReady: make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(q)
	}
	return q, nil
}

// InboxReady is signalled after a Put that stored a row. The channel has a
// buffer of one and the signal never blocks, so several Puts may collapse
// into one signal: drain the inbox with Lease until ErrEmpty.
func (q *Queue) InboxReady() <-chan struct{} { return q.inboxReady }

// OutboxReady is signalled after a Complete or Enqueue that stored a
// message, with the same semantics as InboxReady.
func (q *Queue) OutboxReady() <-chan struct{} { return q.outboxReady }

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// State returns the stored state of conv, or nil if there is none.
func (q *Queue) State(ctx context.Context, conv string) (json.RawMessage, error) {
	var data string
	err := q.db.QueryRowContext(ctx, `SELECT data FROM conv_state WHERE conv = ?`, conv).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sqlqueue: state of %s: %w", conv, err)
	}
	return json.RawMessage(data), nil
}

// Prune deletes inbox rows that are done or expired and outbox rows that
// are sent or failed, finished more than olderThan ago, and the refs of
// delivered messages sent more than refsOlderThan ago. Pending rows are never
// deleted. It returns the number of rows removed.
func (q *Queue) Prune(ctx context.Context, olderThan, refsOlderThan time.Duration) (int, error) {
	now := q.now()
	var total int64
	for _, p := range []struct {
		stmt   string
		cutoff int64
	}{
		{`DELETE FROM inbox WHERE status IN ('done', 'expired') AND done_ms < ?`, now.Add(-olderThan).UnixMilli()},
		{`DELETE FROM outbox WHERE status IN ('sent', 'failed') AND done_ms < ?`, now.Add(-olderThan).UnixMilli()},
		{`DELETE FROM outbox_refs WHERE sent_ms < ?`, now.Add(-refsOlderThan).UnixMilli()},
	} {
		res, err := q.db.ExecContext(ctx, p.stmt, p.cutoff)
		if err != nil {
			return int(total), fmt.Errorf("sqlqueue: prune: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return int(total), err
		}
		total += n
	}
	return int(total), nil
}

// immediate runs fn in a BEGIN IMMEDIATE transaction (sqlitedb.Immediate).
func (q *Queue) immediate(ctx context.Context, fn func(c *sql.Conn) error) error {
	return sqlitedb.Immediate(ctx, q.db, fn)
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}
