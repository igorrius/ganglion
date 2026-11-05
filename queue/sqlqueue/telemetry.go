package sqlqueue

import (
	"context"
	"database/sql"

	"github.com/igorrius/ganglion/sqlitedb"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// takeOutcome says what Complete did with an id in StateUpdate.Taken.
type takeOutcome string

const (
	takeTaken   takeOutcome = "taken"   // a pending event of the conversation, now done
	takeIgnored takeOutcome = "ignored" // anything else; the agent and the queue disagree
)

// takenCounter counts the events turns took from behind them (R-OBS-8).
func takenCounter() (metric.Int64Counter, error) {
	return otel.Meter("ganglion/queue").Int64Counter("ganglion.inbox.taken",
		metric.WithDescription("Events a turn took from behind it, by outcome."))
}

// countTaken adds n events with the outcome; zero adds no series.
func (q *Queue) countTaken(ctx context.Context, outcome takeOutcome, n int) {
	if n > 0 {
		q.taken.Add(ctx, int64(n), metric.WithAttributes(attribute.String("outcome", string(outcome))))
	}
}

// observeDepths registers the depth of the inbox and the outbox as
// gauges (R-OBS-8): events waiting for a turn, messages waiting to be
// delivered. Both counts use the tables' status indexes.
func observeDepths(db *sql.DB) error {
	m := otel.Meter("ganglion/queue")
	if err := sqlitedb.ObserveCount(m, db, "ganglion.inbox.pending", "Events waiting in the inbox.",
		`SELECT COUNT(*) FROM inbox WHERE status = 'pending'`); err != nil {
		return err
	}
	return sqlitedb.ObserveCount(m, db, "ganglion.outbox.pending", "Messages waiting in the outbox.",
		`SELECT COUNT(*) FROM outbox WHERE status = 'pending'`)
}
