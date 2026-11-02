package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

// observeCall records a model call's metrics and ends its span. Nothing
// from the prompt or the answer goes into them.
func (a *Agent) observeCall(ctx context.Context, span trace.Span, c router.Candidate, u provider.Usage, err error, d time.Duration) {
	result := "ok"
	if err != nil {
		result = errorKind(err)
		span.SetStatus(codes.Error, result)
	}
	model := []attribute.KeyValue{attribute.String("provider", c.Provider), attribute.String("model", c.Model)}
	a.in.calls.Add(ctx, 1, metric.WithAttributes(append(model, attribute.String("result", result))...))
	a.in.callDur.Record(ctx, d.Seconds(), metric.WithAttributes(model...))
	if err == nil {
		for kind, n := range map[string]int{"input": u.InputTokens, "cached": u.CacheReadTokens, "output": u.OutputTokens} {
			a.in.tokens.Add(ctx, int64(n), metric.WithAttributes(append(model, attribute.String("kind", kind))...))
		}
		a.in.cost.Add(ctx, c.Cost(u.InputTokens, u.OutputTokens), metric.WithAttributes(model...))
		span.SetAttributes(attribute.Int("tokens.input", u.InputTokens), attribute.Int("tokens.cached", u.CacheReadTokens), attribute.Int("tokens.output", u.OutputTokens))
	}
	span.SetAttributes(attribute.String("result", result))
	span.End()
}

// logTurn writes one info line per turn: what an operator watches in the
// console. Never message text.
func (a *Agent) logTurn(ctx context.Context, r *turns.Turn) {
	model := ""
	for _, at := range r.Route {
		if at.Error == "" {
			model = at.Provider + "/" + at.Model
		}
	}
	dur := r.Finished.Sub(r.Started)
	outcome := attribute.String("outcome", string(r.Outcome))
	a.in.turns.Add(ctx, 1, metric.WithAttributes(outcome))
	a.in.turnDur.Record(ctx, dur.Seconds(), metric.WithAttributes(outcome))
	trace.SpanFromContext(ctx).SetAttributes(outcome, attribute.Int("steps", r.Steps))
	a.log.InfoContext(ctx, "agent: turn", "id", r.EventID, "conv", r.Conv, "principal", r.Principal, "outcome", r.Outcome,
		"steps", r.Steps, "model", model, "attempts", len(r.Route), "tools", len(r.Tools),
		"in_tokens", r.InputTokens, "cached_tokens", r.CacheReadTokens, "out_tokens", r.OutputTokens,
		"cost_usd", fmt.Sprintf("%.6f", r.CostUSD), "ms", dur.Milliseconds())
}
