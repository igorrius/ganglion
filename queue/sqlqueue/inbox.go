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

// Put implements queue.Queue. An empty ID gets a fresh ULID; a zero At is
// set to now, so a TTL counts from the moment the event was stored.
func (q *Queue) Put(ctx context.Context, e event.Event) error {
	if e.IdemKey == "" || e.Conv == "" {
		return fmt.Errorf("sqlqueue: put: event needs an IdemKey and a Conv")
	}
	now := q.now()
	if e.ID == "" {
		e.ID = event.NewID(now)
	}
	if e.At.IsZero() {
		e.At = now
	}
	var payload any
	if e.Payload != nil {
		payload = string(e.Payload)
	}
	res, err := q.db.ExecContext(ctx,
		`INSERT INTO inbox (id, idem_key, source, conv, kind, payload, ttl_ms, at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (idem_key) DO NOTHING`,
		e.ID, e.IdemKey, e.Source, e.Conv, e.Kind, payload, e.TTL.Milliseconds(), e.At.UnixMilli())
	if err != nil {
		return fmt.Errorf("sqlqueue: put %s: %w", e.IdemKey, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		signal(q.inboxReady)
	}
	return nil
}

// Lease implements queue.Queue. It takes the oldest pending event that
// heads its conversation and is not leased, so a conversation is processed
// in order and one event at a time. Expired heads are marked expired and
// skipped on the way.
func (q *Queue) Lease(ctx context.Context, d time.Duration) (queue.Lease, error) {
	if d <= 0 {
		return queue.Lease{}, fmt.Errorf("sqlqueue: lease duration %v must be positive", d)
	}
	var l queue.Lease
	err := q.immediate(ctx, func(c *sql.Conn) error {
		now := q.now()
		for {
			e, seq, err := nextHead(ctx, c, now)
			if err != nil {
				return err
			}
			if e.Expired(now) {
				if _, err := c.ExecContext(ctx,
					`UPDATE inbox SET status = 'expired', done_ms = ? WHERE seq = ?`, now.UnixMilli(), seq); err != nil {
					return err
				}
				continue
			}
			l = queue.Lease{Event: e, Token: event.NewID(now), Until: now.Add(d)}
			_, err = c.ExecContext(ctx,
				`UPDATE inbox SET lease_token = ?, lease_until_ms = ?, attempts = attempts + 1 WHERE seq = ?`,
				l.Token, l.Until.UnixMilli(), seq)
			return err
		}
	})
	if err != nil {
		if errors.Is(err, queue.ErrEmpty) {
			return queue.Lease{}, err
		}
		return queue.Lease{}, fmt.Errorf("sqlqueue: lease: %w", err)
	}
	return l, nil
}

// nextHead returns the oldest pending, unleased event that heads its
// conversation, or queue.ErrEmpty.
func nextHead(ctx context.Context, c *sql.Conn, now time.Time) (event.Event, int64, error) {
	var (
		e           event.Event
		seq         int64
		payload     sql.NullString
		ttlMS, atMS int64
	)
	err := c.QueryRowContext(ctx,
		`SELECT i.seq, i.id, i.idem_key, i.source, i.conv, i.kind, i.payload, i.ttl_ms, i.at_ms
		 FROM inbox i
		 WHERE i.status = 'pending' AND i.lease_until_ms <= ?
		   AND NOT EXISTS (SELECT 1 FROM inbox j
		                   WHERE j.conv = i.conv AND j.status = 'pending' AND j.seq < i.seq)
		 ORDER BY i.seq LIMIT 1`, now.UnixMilli(),
	).Scan(&seq, &e.ID, &e.IdemKey, &e.Source, &e.Conv, &e.Kind, &payload, &ttlMS, &atMS)
	if errors.Is(err, sql.ErrNoRows) {
		return event.Event{}, 0, queue.ErrEmpty
	}
	if err != nil {
		return event.Event{}, 0, err
	}
	if payload.Valid {
		e.Payload = json.RawMessage(payload.String)
	}
	e.TTL = time.Duration(ttlMS) * time.Millisecond
	e.At = fromMS(atMS)
	return e, seq, nil
}

// Complete implements queue.Queue. In one transaction it marks the event
// done, stores the replies (a reply whose IdemKey is already stored is
// skipped) and upserts the conversation state when st.Conv is set. A lease
// that ran out but was not taken by anyone else still completes.
func (q *Queue) Complete(ctx context.Context, l queue.Lease, out []event.Outgoing, st queue.StateUpdate) error {
	stored := false
	err := q.immediate(ctx, func(c *sql.Conn) error {
		now := q.now()
		res, err := c.ExecContext(ctx,
			`UPDATE inbox SET status = 'done', done_ms = ?, lease_until_ms = 0
			 WHERE id = ? AND lease_token = ? AND status = 'pending'`,
			now.UnixMilli(), l.Event.ID, l.Token)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return queue.ErrLeaseLost
		}
		for _, m := range out {
			ok, err := insertOutgoing(ctx, c, m, now)
			if err != nil {
				return err
			}
			stored = stored || ok
		}
		if st.Conv != "" {
			var data any
			if st.Data != nil {
				data = string(st.Data)
			}
			if _, err := c.ExecContext(ctx,
				`INSERT INTO conv_state (conv, data, updated_ms) VALUES (?, ?, ?)
				 ON CONFLICT (conv) DO UPDATE SET data = excluded.data, updated_ms = excluded.updated_ms`,
				st.Conv, data, now.UnixMilli()); err != nil {
				return fmt.Errorf("state of %s: %w", st.Conv, err)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, queue.ErrLeaseLost) {
			return err
		}
		return fmt.Errorf("sqlqueue: complete %s: %w", l.Event.ID, err)
	}
	if stored {
		signal(q.outboxReady)
	}
	return nil
}

// insertOutgoing stores m in the outbox unless its IdemKey is already
// there, and reports whether it stored a row.
func insertOutgoing(ctx context.Context, c *sql.Conn, m event.Outgoing, now time.Time) (bool, error) {
	if m.IdemKey == "" || m.Transport == "" || m.Conv == "" {
		return false, fmt.Errorf("outgoing message needs an IdemKey, a Transport and a Conv")
	}
	if m.ID == "" {
		m.ID = event.NewID(now)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	res, err := c.ExecContext(ctx,
		`INSERT INTO outbox (id, idem_key, transport, conv, body, created_ms) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (idem_key) DO NOTHING`,
		m.ID, m.IdemKey, m.Transport, m.Conv, string(body), now.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("outbox %s: %w", m.IdemKey, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReleaseLeases clears the lease of every pending event, so events taken
// by a process that died are leasable at once. Call it when the one core
// process that owns this database starts, before its loop runs
// (ADR-0019). A cleared token makes a late Complete by the old holder
// return queue.ErrLeaseLost. It returns how many leases it released.
func (q *Queue) ReleaseLeases(ctx context.Context) (int, error) {
	res, err := q.db.ExecContext(ctx,
		`UPDATE inbox SET lease_token = '', lease_until_ms = 0
		 WHERE status = 'pending' AND lease_token != ''`)
	if err != nil {
		return 0, fmt.Errorf("sqlqueue: release leases: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
