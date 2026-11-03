package schedule

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments are the scheduler's metrics (R-OBS-8), labelled by the
// job's kind: a closed set.
type instruments struct {
	runs metric.Int64Counter
	lag  metric.Float64Histogram
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/schedule")
	var in instruments
	in.runs, _ = m.Int64Counter("ganglion.schedule.runs",
		metric.WithDescription("Job events put into the inbox, scheduled or run now, by kind and result (ok, error)."))
	in.lag, _ = m.Float64Histogram("ganglion.schedule.lag", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("How long after its slot a scheduled run's event was put, by kind."))
	return in
}

// observeRun counts one put of a job's event.
func (in instruments) observeRun(ctx context.Context, k Kind, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	in.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(k)), attribute.String("result", result)))
}

// observeLag records how late a scheduled run was put.
func (in instruments) observeLag(ctx context.Context, k Kind, lag time.Duration) {
	in.lag.Record(ctx, max(lag, 0).Seconds(), metric.WithAttributes(attribute.String("kind", string(k))))
}
