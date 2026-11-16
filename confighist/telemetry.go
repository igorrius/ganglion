package confighist

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// newChanges is the counter of recorded changes by source (a closed set).
func newChanges() metric.Int64Counter {
	c, _ := otel.Meter("ganglion/confighist").Int64Counter("ganglion.config.changes",
		metric.WithDescription("Changes of the node's file recorded in the workspace, by source."))
	return c
}

// recorded counts one recorded change.
func (r *Recorder) recorded(ctx context.Context, src Source) {
	r.changes.Add(ctx, 1, metric.WithAttributes(attribute.String("source", string(src))))
}
