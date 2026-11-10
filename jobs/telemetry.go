package jobs

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// editResult is what became of a card's edit, the label set of
// ganglion.jobs.card.edits.
type editResult string

// Results of an edit.
const (
	editSent      editResult = "sent"      // stored in the outbox
	editUnchanged editResult = "unchanged" // its text did not change, so none was sent
	editFailed    editResult = "failed"    // the outbox or the row refused it
)

// telemetry is the registry's metrics (R-OBS-8): jobs by how they ended, the
// cards' edits, and the jobs open now.
type telemetry struct {
	ended metric.Int64Counter
	edits metric.Int64Counter
}

// newTelemetry makes the instruments; the open gauge reads open.
func newTelemetry(open func() int) telemetry {
	m := otel.Meter("ganglion/jobs")
	var t telemetry
	t.ended, _ = m.Int64Counter("ganglion.jobs.ended",
		metric.WithDescription("Background jobs that ended, by kind and state: done, failed, interrupted."))
	t.edits, _ = m.Int64Counter("ganglion.jobs.card.edits",
		metric.WithDescription("Edits of background jobs' cards, by result: sent, unchanged, failed."))
	_, _ = m.Int64ObservableGauge("ganglion.jobs.open",
		metric.WithDescription("Background jobs running or queued now."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(open()))
			return nil
		}))
	return t
}

// end counts a job's end.
func (t telemetry) end(ctx context.Context, k Kind, s State) {
	t.ended.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(k)), attribute.String("state", string(s))))
}

// edit counts an edit's result.
func (t telemetry) edit(ctx context.Context, r editResult) {
	t.edits.Add(ctx, 1, metric.WithAttributes(attribute.String("result", string(r))))
}
