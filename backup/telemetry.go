package backup

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcomeOK is the outcome label of a backup that succeeded; a failed one
// is labelled with its Failure, a closed set (R-OBS-8).
const outcomeOK = "ok"

// telemetry is the daily backup's metrics (R-OBS-8): a counter of runs by
// outcome, and gauges of the last success's unix time and the size of its
// object.
type telemetry struct {
	runs        metric.Int64Counter
	lastSuccess metric.Int64Gauge
	lastSize    metric.Int64Gauge
}

func newTelemetry() telemetry {
	m := otel.Meter("ganglion/backup")
	var t telemetry
	t.runs, _ = m.Int64Counter("ganglion.backup.runs",
		metric.WithDescription("Daily backups, by outcome: ok, or why it failed: refused, network, changed, integrity, other."))
	t.lastSuccess, _ = m.Int64Gauge("ganglion.backup.last_success", metric.WithUnit("s"),
		metric.WithDescription("The time of the last successful backup, as Unix seconds; 0 until one is known."))
	t.lastSize, _ = m.Int64Gauge("ganglion.backup.last_size", metric.WithUnit("By"),
		metric.WithDescription("The size of the last successful backup's object."))
	return t
}

// ran counts a finished run and, for a success, moves the gauges.
func (t telemetry) ran(ctx context.Context, r Result) {
	outcome := outcomeOK
	if !r.OK {
		outcome = string(r.Kind)
		if outcome == "" {
			outcome = string(FailureOther)
		}
	}
	t.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	if r.OK {
		t.last(ctx, r)
	}
}

// last sets the gauges to the last success r.
func (t telemetry) last(ctx context.Context, r Result) {
	t.lastSuccess.Record(ctx, r.LastOK.Unix())
	t.lastSize.Record(ctx, r.Size)
}
