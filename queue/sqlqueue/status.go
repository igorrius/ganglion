package sqlqueue

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

const (
	// queueOld is how long the oldest waiting message may wait before /status warns.
	queueOld = time.Minute
	// failedSeen is how far back /status counts undelivered messages.
	failedSeen = 24 * time.Hour
)

// Name implements status.Source.
func (*Queue) Name() string { return "queues" }

// Status implements status.Source (R-OBS-11): what waits in the inbox (a
// message being handled, under its lease, does not wait) and the outbox, how long the oldest has waited, and what was not delivered in a day.
func (q *Queue) Status(ctx context.Context, s status.Scope) []status.Check {
	now := s.Now.UnixMilli()
	in, inOldest, err := q.waiting(ctx, `SELECT count(*), MIN(at_ms) FROM inbox
		WHERE status = 'pending' AND at_ms <= ? AND lease_until_ms <= ?`, now, now)
	if err != nil {
		return []status.Check{silent()}
	}
	out, outOldest, err := q.waiting(ctx, `SELECT count(*), MIN(created_ms) FROM outbox WHERE status = 'pending'`)
	if err != nil {
		return []status.Check{silent()}
	}
	var failed int
	if err := q.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE status = 'failed' AND done_ms >= ?`,
		s.Now.Add(-failedSeen).UnixMilli()).Scan(&failed); err != nil {
		return []status.Check{silent()}
	}
	c := status.Check{Section: status.SectionDelivery, State: status.OK, Line: texts.StatusQueues,
		Vars: texts.Vars{"inbox": strconv.Itoa(in), "outbox": strconv.Itoa(out)}}
	oldest := earliest(inOldest, outOldest)
	switch {
	case !oldest.IsZero() && s.Now.Sub(oldest) > queueOld:
		c.State, c.Reason, c.Since = status.Warn, texts.StatusReasonQueueOld, oldest
		c.Vars["for"] = status.Duration(s.Now.Sub(oldest))
	case failed > 0:
		c.State, c.Reason = status.Warn, texts.StatusReasonUndelivered
		c.Vars["n"] = strconv.Itoa(failed)
	}
	return []status.Check{c}
}

// waiting runs a count-and-oldest query.
func (q *Queue) waiting(ctx context.Context, query string, args ...any) (int, time.Time, error) {
	var n int
	var oldest sql.NullInt64
	if err := q.db.QueryRowContext(ctx, query, args...).Scan(&n, &oldest); err != nil {
		return 0, time.Time{}, fmt.Errorf("sqlqueue: status: %w", err)
	}
	if !oldest.Valid {
		return n, time.Time{}, nil
	}
	return n, time.UnixMilli(oldest.Int64).UTC(), nil
}

// earliest is the earlier of two times, a zero one left out.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	default:
		return b
	}
}

func silent() status.Check {
	return status.Check{Section: status.SectionDelivery, State: status.Warn, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": "queues"}}
}
