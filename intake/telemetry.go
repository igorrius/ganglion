package intake

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// telemetry are the registry's instruments (R-OBS-8). Labels come from
// closed sets: a line's outcome, a request's kind and outcome.
type telemetry struct {
	lines    metric.Int64Counter
	requests metric.Int64Counter
}

// newTelemetry makes the instruments; the open gauge reads open.
func newTelemetry(open func() int) telemetry {
	m := otel.Meter("ganglion/intake")
	var t telemetry
	t.lines, _ = m.Int64Counter("ganglion.intake.lines",
		metric.WithDescription("Pasted intake lines the transport took, by outcome (taken, not_private, not_admin, malformed, version, unknown, expired, failed, dropped)."))
	t.requests, _ = m.Int64Counter("ganglion.intake.requests",
		metric.WithDescription("Intake requests that closed, by kind (oauth, secret) and outcome (answered, failed, expired)."))
	// The callback is registered apart from the instrument: the SDK keeps
	// only the first callback passed when an instrument is made, and a
	// second registry (a test's) would be lost.
	g, _ := m.Int64ObservableGauge("ganglion.intake.open", metric.WithDescription("Intake requests open now."))
	_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(g, int64(open()))
		return nil
	}, g)
	return t
}

func (t telemetry) line(ctx context.Context, o TakeOutcome) {
	t.lines.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}

func (t telemetry) closed(ctx context.Context, k Kind, o Outcome) {
	t.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(k)), attribute.String("outcome", string(o))))
}

// pageTelemetry is the page check's instrument (R-OBS-8).
type pageTelemetry struct {
	checks metric.Int64Counter
}

func newPageTelemetry() pageTelemetry {
	var t pageTelemetry
	t.checks, _ = otel.Meter("ganglion/intake").Int64Counter("ganglion.intake.page_checks",
		metric.WithDescription("Checks of the hosted intake page against the release's copy, by outcome (same, changed, unchecked)."))
	return t
}

func (t pageTelemetry) checked(ctx context.Context, o PageOutcome) {
	t.checks.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
