package sqlqueue

import (
	"database/sql"

	"github.com/igorrius/ganglion/sqlitedb"
	"go.opentelemetry.io/otel"
)

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
