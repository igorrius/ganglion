package sqlqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// PartSent implements queue.Outbox. The count never goes down; ref, when not "",
// becomes the message's ref, and shown is noted for SentByRef.
func (q *Queue) PartSent(ctx context.Context, id string, parts int, ref, shown string) error {
	return q.immediate(ctx, func(c *sql.Conn) error {
		res, err := c.ExecContext(ctx,
			`UPDATE outbox SET parts_sent = max(parts_sent, ?), ref = CASE WHEN ? = '' THEN ref ELSE ? END
			 WHERE id = ? AND status = 'pending'`, parts, ref, ref, id)
		if err := onePending(res, err); err != nil {
			return fmt.Errorf("sqlqueue: part sent %s: %w", id, err)
		}
		return noteRef(ctx, c, id, shown, q.now().UnixMilli())
	})
}

// refTextRunes is how much of a message's text its ref keeps for a reply
// (R-DLV-13); the agent cuts what it shows the model to the same length.
const refTextRunes = 2000

// noteRef points ref at the outbox row id and copies what a reply needs from
// it, its text cut at refTextRunes and its origin, with the time now, so the
// ref outlives the row (RefRetention); "" is no ref. A ref noted again, by an
// edit with text, takes the edit's copy.
func noteRef(ctx context.Context, c *sql.Conn, id, ref string, now int64) error {
	if ref == "" {
		return nil
	}
	_, err := c.ExecContext(ctx,
		`INSERT INTO outbox_refs (transport, conv, ref, outbox_id, text, origin, sent_ms)
		 SELECT transport, conv, ?, id, coalesce(substr(json_extract(body, '$.Text'), 1, ?), ''),
		        json_extract(body, '$.Origin'), ?
		 FROM outbox WHERE id = ?
		 ON CONFLICT (transport, conv, ref) DO UPDATE SET outbox_id = excluded.outbox_id,
		   text = excluded.text, origin = excluded.origin, sent_ms = excluded.sent_ms`,
		ref, refTextRunes, now, id)
	if err != nil {
		return fmt.Errorf("sqlqueue: ref of %s: %w", id, err)
	}
	return nil
}

// SentByRef implements queue.Outbox. It reads the copy the ref keeps, so it
// finds a message the outbox has pruned: its Text and Origin, nothing else.
func (q *Queue) SentByRef(ctx context.Context, transport, conv, ref string) (event.Outgoing, bool, error) {
	var (
		m      event.Outgoing
		origin sql.NullString
	)
	err := q.db.QueryRowContext(ctx,
		`SELECT text, origin FROM outbox_refs WHERE transport = ? AND conv = ? AND ref = ?`,
		transport, conv, ref).Scan(&m.Text, &origin)
	if errors.Is(err, sql.ErrNoRows) {
		return event.Outgoing{}, false, nil
	}
	if err != nil {
		return event.Outgoing{}, false, fmt.Errorf("sqlqueue: message %s in %s: %w", ref, conv, err)
	}
	if origin.Valid {
		m.Origin = new(event.Origin)
		if err := json.Unmarshal([]byte(origin.String), m.Origin); err != nil {
			return event.Outgoing{}, false, fmt.Errorf("sqlqueue: origin of message %s in %s: %w", ref, conv, err)
		}
	}
	return m, true, nil
}

// Sent implements queue.Outbox. A ref of "" keeps the one a part recorded, so a
// message resumed after its last part was recorded keeps that part's ref.
func (q *Queue) Sent(ctx context.Context, id, ref, shown string) error {
	return q.immediate(ctx, func(c *sql.Conn) error {
		now := q.now().UnixMilli()
		res, err := c.ExecContext(ctx,
			`UPDATE outbox SET status = 'sent', done_ms = ?, ref = CASE WHEN ? = '' THEN ref ELSE ? END
			 WHERE id = ? AND status = 'pending'`, now, ref, ref, id)
		if err := onePending(res, err); err != nil {
			return fmt.Errorf("sqlqueue: sent %s: %w", id, err)
		}
		return noteRef(ctx, c, id, shown, now)
	})
}

// SentRef implements queue.Outbox.
func (q *Queue) SentRef(ctx context.Context, idemKey string) (string, error) {
	var ref string
	err := q.db.QueryRowContext(ctx, `SELECT ref FROM outbox WHERE idem_key = ? AND status = 'sent'`, idemKey).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlqueue: ref of %s: %w", idemKey, err)
	}
	return ref, nil
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
	if err := onePending(res, err); err != nil {
		return fmt.Errorf("sqlqueue: %s %s: %w", op, id, err)
	}
	return nil
}

// onePending is the outcome of an update of one pending outbox row: err, or an
// error when it changed no row because the row is missing or already finished.
func onePending(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no pending message with this id")
	}
	return nil
}
