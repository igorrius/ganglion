package host

import (
	"context"

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
