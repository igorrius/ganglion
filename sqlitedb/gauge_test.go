package sqlitedb

import (
	"context"
	"testing"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ObserveCount reads its query at every collection, so the gauge follows
// the table.
func TestObserveCount(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	db, err := Open(ctx, openFile(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TABLE q (status TEXT); INSERT INTO q VALUES ('pending'), ('pending'), ('done')`); err != nil {
		t.Fatal(err)
	}
	if err := ObserveCount(otel.Meter("ganglion/test"), db, "ganglion.test.pending", "Rows pending.",
		`SELECT COUNT(*) FROM q WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if got := rec.Value(t, "ganglion.test.pending", nil); got != 2 {
		t.Fatalf("gauge = %v, want 2", got)
	}
	if _, err := db.ExecContext(ctx, `UPDATE q SET status = 'done'`); err != nil {
		t.Fatal(err)
	}
	if got := rec.Value(t, "ganglion.test.pending", nil); got != 0 {
		t.Fatalf("gauge after the update = %v, want 0", got)
	}
}

// A second gauge of the same name still reads its own query: the SDK
// ignores callbacks passed when an instrument is made the second time.
func TestObserveCountTwice(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	for _, n := range []int{1, 2} {
		db, err := Open(ctx, openFile(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE q (status TEXT)`); err != nil {
			t.Fatal(err)
		}
		for range n {
			if _, err := db.ExecContext(ctx, `INSERT INTO q VALUES ('pending')`); err != nil {
				t.Fatal(err)
			}
		}
		if err := ObserveCount(otel.Meter("ganglion/test"), db, "ganglion.test.twice", "Rows pending.",
			`SELECT COUNT(*) FROM q WHERE status = 'pending'`); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			_ = db.Close() // the first owner is gone, as an earlier test's store is
			continue
		}
		t.Cleanup(func() { _ = db.Close() })
	}
	if got := rec.Value(t, "ganglion.test.twice", nil); got != 2 {
		t.Fatalf("gauge = %v, want the second database's 2", got)
	}
}

// A gauge over a database that has been closed (the node stopping while
// something still reads the metrics) observes nothing and reports no
// error: the owner's stop log showed "sql: database is closed" every
// scrape.
func TestObserveCountAfterClose(t *testing.T) {
	telemetrytest.Install(t)
	ctx := context.Background()
	db, err := Open(ctx, openFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE q (status TEXT)`); err != nil {
		t.Fatal(err)
	}
	m := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(m))
	defer func() { _ = mp.Shutdown(ctx) }()
	if err := ObserveCount(mp.Meter("t"), db, "ganglion.test.closed", "x", `SELECT COUNT(*) FROM q`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var rm metricdata.ResourceMetrics
	if err := m.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect after close: %v", err)
	}
}
