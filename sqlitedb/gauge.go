package sqlitedb

import (
	"context"
	"database/sql"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// countTimeout bounds the query behind a gauge: a scrape never waits on
// a busy database for long.
const countTimeout = time.Second

// ObserveCount registers name as a gauge read by query, a SELECT COUNT(*)
// of rows in one state (a queue's depth), each time the metrics are
// collected (R-OBS-8). A failed query observes nothing, rather than a
// wrong level. The callback is registered on its own, not passed when
// the gauge is made: the SDK keeps only the first such callback of an
// instrument, and a second owner of the same name would be lost.
func ObserveCount(m metric.Meter, db *sql.DB, name, description, query string) error {
	g, err := m.Int64ObservableGauge(name, metric.WithDescription(description))
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, countTimeout)
		defer cancel()
		var n int64
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return err
		}
		o.ObserveInt64(g, n)
		return nil
	}, g)
	return err
}
