package agent

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instruments are the agent's metrics and tracer (R-OBS-1). Labels stay
// low-cardinality: never a conversation or a principal.
type instruments struct {
	tracer  trace.Tracer
	turns   metric.Int64Counter
	turnDur metric.Float64Histogram
	calls   metric.Int64Counter
	callDur metric.Float64Histogram
	tokens  metric.Int64Counter
	cost    metric.Float64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/agent")
	in := instruments{tracer: otel.Tracer("ganglion/agent")}
	in.turns, _ = m.Int64Counter("ganglion.turns", metric.WithDescription("Turns handled, by outcome."))
	in.turnDur, _ = m.Float64Histogram("ganglion.turn.duration", metric.WithUnit("s"), metric.WithDescription("Turn duration, by outcome."))
	in.calls, _ = m.Int64Counter("ganglion.model.calls", metric.WithDescription("Model calls, by provider, model and result."))
	in.callDur, _ = m.Float64Histogram("ganglion.model.call.duration", metric.WithUnit("s"), metric.WithDescription("Model call duration."))
	in.tokens, _ = m.Int64Counter("ganglion.model.tokens", metric.WithDescription("Tokens by kind: input (cached included), cached, output."))
	in.cost, _ = m.Float64Counter("ganglion.model.cost_usd", metric.WithDescription("Model cost in USD at catalogue prices."))
	return in
}
