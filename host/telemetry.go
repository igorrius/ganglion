package host

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// janitorTelemetry is the janitor's metric: what it cleaned (R-OBS-8).
type janitorTelemetry struct {
	cleanedBytes metric.Int64Counter
}

func newJanitorTelemetry() janitorTelemetry {
	m := otel.Meter("ganglion/host")
	var t janitorTelemetry
	t.cleanedBytes, _ = m.Int64Counter("ganglion.host.cleaned", metric.WithUnit("By"),
		metric.WithDescription("Bytes the janitor removed from the node's places, by place."))
	return t
}

// cleaned counts the bytes of one removed entry. PlaceName is a closed set,
// so it may be a label.
func (t janitorTelemetry) cleaned(ctx context.Context, p PlaceName, bytes int64) {
	t.cleanedBytes.Add(ctx, bytes, metric.WithAttributes(attribute.String("place", string(p))))
}

// runnerTelemetry is the runner's metrics: how commands ended, how long
// they took, and how many run now (R-OBS-8).
type runnerTelemetry struct {
	commands metric.Int64Counter
	duration metric.Float64Histogram
}

// newRunnerTelemetry makes the instruments; the running gauge reads the
// slots held.
func newRunnerTelemetry(slots chan struct{}) runnerTelemetry {
	m := otel.Meter("ganglion/host")
	var t runnerTelemetry
	t.commands, _ = m.Int64Counter("ganglion.host.commands",
		metric.WithDescription("Host commands that ended, by outcome: ok, failed, timeout, not_run, interrupted."))
	t.duration, _ = m.Float64Histogram("ganglion.host.command.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("How long a host command took from its call to its end, slot wait included."))
	_, _ = m.Int64ObservableGauge("ganglion.host.commands.running",
		metric.WithDescription("Host commands running now, each holding a slot."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(slots)))
			return nil
		}))
	return t
}

// ended counts a command's end and its time.
func (t runnerTelemetry) ended(ctx context.Context, how outcome, took time.Duration) {
	t.commands.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(how))))
	t.duration.Record(ctx, took.Seconds())
}

// interrupted counts the commands a restart found running.
func (t runnerTelemetry) interrupted(ctx context.Context, n int64) {
	if n > 0 {
		t.commands.Add(ctx, n, metric.WithAttributes(attribute.String("outcome", string(outcomeInterrupted))))
	}
}
