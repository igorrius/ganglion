package sqlqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
)

var _ queue.Outbox = (*Queue)(nil)

// Enqueue implements queue.Outbox. All messages are stored in one
// transaction: an invalid message stores none of them.
func (q *Queue) Enqueue(ctx context.Context, msgs ...event.Outgoing) error {
	stored := false
	err := q.immediate(ctx, func(c *sql.Conn) error {
		now := q.now()
		for _, m := range msgs {
			ok, err := insertOutgoing(ctx, c, m, now)
			if err != nil {
				return err
			}
			stored = stored || ok
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sqlqueue: enqueue: %w", err)
	}
	if stored {
		signal(q.outboxReady)
	}
	return nil
}

// Due implements queue.Outbox. The head of a conversation is its oldest
// pending message on a transport; a head that is not due yet blocks the
// messages behind it.
func (q *Queue) Due(ctx context.Context, limit int) ([]queue.Pending, time.Time, error) {
	now := q.now().UnixMilli()
	rows, err := q.db.QueryContext(ctx,
		`SELECT o.id, o.body, o.attempts, o.parts_sent, o.not_before_ms
		 FROM outbox o
		 WHERE o.status = 'pending'
		   AND NOT EXISTS (SELECT 1 FROM outbox p
		                   WHERE p.transport = o.transport AND p.conv = o.conv
		                     AND p.status = 'pending' AND p.seq < o.seq)
		 ORDER BY o.seq`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("sqlqueue: due: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		due  []queue.Pending
		next int64
	)
	for rows.Next() {
		var (
			id, body  string
			p         queue.Pending
			notBefore int64
		)
		if err := rows.Scan(&id, &body, &p.Attempts, &p.PartsSent, &notBefore); err != nil {
			return nil, time.Time{}, fmt.Errorf("sqlqueue: due: %w", err)
		}
		if notBefore > now {
			if next == 0 || notBefore < next {
				next = notBefore
			}
			continue
		}
		if len(due) >= limit {
			continue
		}
		if err := json.Unmarshal([]byte(body), &p.Msg); err != nil {
			return nil, time.Time{}, fmt.Errorf("sqlqueue: due: body of %s: %w", id, err)
		}
		p.Msg.ID = id
		due = append(due, p)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("sqlqueue: due: %w", err)
	}
	return due, fromMS(next), nil
}

// PartSent implements queue.Outbox. The count never goes down.
func (q *Queue) PartSent(ctx context.Context, id string, parts int) error {
	return q.updatePending(ctx, "part sent", id,
		`UPDATE outbox SET parts_sent = max(parts_sent, ?) WHERE id = ? AND status = 'pending'`, parts, id)
}

// Sent implements queue.Outbox.
func (q *Queue) Sent(ctx context.Context, id string) error {
	return q.updatePending(ctx, "sent", id,
		`UPDATE outbox SET status = 'sent', done_ms = ? WHERE id = ? AND status = 'pending'`,
		q.now().UnixMilli(), id)
}

// Retry implements queue.Outbox.
func (q *Queue) Retry(ctx context.Context, id string, notBefore time.Time, reason string) error {
	return q.updatePending(ctx, "retry", id,
		`UPDATE outbox SET attempts = attempts + 1, not_before_ms = ?, last_error = ?
		 WHERE id = ? AND status = 'pending'`,
		notBefore.UnixMilli(), reason, id)
}

// Fail implements queue.Outbox.
func (q *Queue) Fail(ctx context.Context, id string, reason string) error {
	return q.updatePending(ctx, "fail", id,
		`UPDATE outbox SET status = 'failed', done_ms = ?, last_error = ? WHERE id = ? AND status = 'pending'`,
		q.now().UnixMilli(), reason, id)
}

// updatePending runs an update of one pending outbox row and fails when
// the row is missing or already finished.
func (q *Queue) updatePending(ctx context.Context, op, id, stmt string, args ...any) error {
	res, err := q.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("sqlqueue: %s %s: %w", op, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlqueue: %s %s: %w", op, id, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlqueue: %s %s: no pending message with this id", op, id)
	}
	return nil
}
