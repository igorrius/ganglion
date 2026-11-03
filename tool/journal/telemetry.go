package journal

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// unknownTool labels a call whose name is not a registered tool: the
// model chose that name, so it never becomes a label (R-OBS-8).
const unknownTool = "unknown"

// instruments are the journal's metrics: what tools run, what the policy
// said and how long they took (R-OBS-8).
type instruments struct {
	calls metric.Int64Counter
	dur   metric.Float64Histogram
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/journal")
	var in instruments
	in.calls, _ = m.Int64Counter("ganglion.tool.calls",
		metric.WithDescription("Tool calls, by tool, policy decision and result (ok, error)."))
	in.dur, _ = m.Float64Histogram("ganglion.tool.call.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("Tool call duration, by tool."))
	return in
}

// observe counts and times one call. Its arguments and result never go
// into the metrics.
func (j *Journal) observe(ctx context.Context, name string, d Decision, err error, took time.Duration) {
	if !j.known(name) {
		name = unknownTool
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	toolName := attribute.String("tool", name)
	j.in.calls.Add(ctx, 1, metric.WithAttributes(toolName, attribute.String("decision", string(d)), attribute.String("result", result)))
	j.in.dur.Record(ctx, took.Seconds(), metric.WithAttributes(toolName))
}
