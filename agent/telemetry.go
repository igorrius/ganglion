package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
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
	tracer       trace.Tracer
	turns        metric.Int64Counter
	turnDur      metric.Float64Histogram
	steps        metric.Int64Histogram
	calls        metric.Int64Counter
	callDur      metric.Float64Histogram
	context      metric.Int64Histogram
	fallbacks    metric.Int64Counter
	tokens       metric.Int64Counter
	cost         metric.Float64Counter
	ended        metric.Int64Counter
	compactions  metric.Int64Counter
	additions    metric.Int64Counter
	skippedCalls metric.Int64Counter
	quotes       metric.Int64Counter
	runsEnded    metric.Int64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/agent")
	in := instruments{tracer: otel.Tracer("ganglion/agent")}
	in.turns, _ = m.Int64Counter("ganglion.turns", metric.WithDescription("Turns handled, by outcome."))
	in.turnDur, _ = m.Float64Histogram("ganglion.turn.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("Turn duration, by outcome."))
	in.steps, _ = m.Int64Histogram("ganglion.turn.steps", buckets.Counts, metric.WithDescription("Model calls per turn, by outcome."))
	in.calls, _ = m.Int64Counter("ganglion.model.calls", metric.WithDescription("Model calls, by provider, model and result."))
	in.callDur, _ = m.Float64Histogram("ganglion.model.call.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("Model call duration."))
	in.context, _ = m.Int64Histogram("ganglion.model.context_tokens", buckets.Tokens,
		metric.WithDescription("Input tokens of a model call that answered: how big the context gets."))
	in.fallbacks, _ = m.Int64Counter("ganglion.model.fallbacks",
		metric.WithDescription("Model calls answered by a later candidate, by the first candidate's error kind."))
	in.tokens, _ = m.Int64Counter("ganglion.model.tokens", metric.WithDescription("Tokens by kind: input (cached included), cached, output."))
	in.cost, _ = m.Float64Counter("ganglion.model.cost_usd", metric.WithDescription("Model cost in USD at catalogue prices."))
	in.ended, _ = m.Int64Counter("ganglion.sessions.ended", metric.WithDescription("Sessions archived, by what ended them: idle, new, resume."))
	in.compactions, _ = m.Int64Counter("ganglion.compactions", metric.WithDescription("History compactions, by result: ok, failed."))
	in.additions, _ = m.Int64Counter("ganglion.additions.taken", metric.WithDescription("Messages that joined a running turn."))
	in.skippedCalls, _ = m.Int64Counter("ganglion.additions.skipped_calls", metric.WithDescription("Tool calls not run because an addition waited."))
	in.quotes, _ = m.Int64Counter("ganglion.additions.quotes", metric.WithDescription("Answers sent as overtaken by a later message, by result: stored, with_turn."))
	in.runsEnded, _ = m.Int64Counter("ganglion.additions.runs_ended", metric.WithDescription("CLI runs ended at a tool boundary for an addition."))
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
		a.in.context.Record(ctx, int64(u.InputTokens), metric.WithAttributes(model...))
		span.SetAttributes(attribute.Int("tokens.input", u.InputTokens), attribute.Int("tokens.cached", u.CacheReadTokens), attribute.Int("tokens.output", u.OutputTokens))
	}
	span.SetAttributes(attribute.String("result", result))
	span.End()
}

// logTurn writes one info line per turn: what an operator watches in the
// console. Never message text.
func (a *Agent) logTurn(ctx context.Context, r *turns.Turn, additions int) {
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
	a.in.steps.Record(ctx, int64(r.Steps), metric.WithAttributes(outcome))
	trace.SpanFromContext(ctx).SetAttributes(outcome, attribute.Int("steps", r.Steps))
	a.log.InfoContext(ctx, "agent: turn", "id", r.EventID, "conv", r.Conv, "principal", r.Principal, "outcome", r.Outcome,
		"steps", r.Steps, "additions", additions, "model", model, "attempts", len(r.Route), "tools", len(r.Tools),
		"in_tokens", r.InputTokens, "cached_tokens", r.CacheReadTokens, "out_tokens", r.OutputTokens,
		"cost_usd", fmt.Sprintf("%.6f", r.CostUSD), "ms", dur.Milliseconds())
}

// observeFallback counts a model call answered by a later candidate;
// reason is the first candidate's error kind.
func (a *Agent) observeFallback(ctx context.Context, reason string) {
	a.in.fallbacks.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// observeEnded counts a session archived; by is a closed set (idle, new,
// resume).
func (a *Agent) observeEnded(ctx context.Context, by string) {
	a.in.ended.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", by)))
}

// observeRunEnded counts a run ended for an addition and what it is estimated
// to have spent: it reports no usage itself (R-LIVE-3).
func (a *Agent) observeRunEnded(ctx context.Context, c router.Candidate, est provider.Usage) {
	model := []attribute.KeyValue{attribute.String("provider", c.Provider), attribute.String("model", c.Model)}
	a.in.runsEnded.Add(ctx, 1, metric.WithAttributes(model...))
	a.in.tokens.Add(ctx, int64(est.InputTokens), metric.WithAttributes(append(model, attribute.String("kind", "input"))...))
	a.in.cost.Add(ctx, c.Cost(est.InputTokens, 0), metric.WithAttributes(model...))
}

// observeQuote counts an answer sent as overtaken by a later message.
func (a *Agent) observeQuote(ctx context.Context, stored bool) {
	result := "stored"
	if !stored {
		result = "with_turn"
	}
	a.in.quotes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// observeCompaction counts a compaction by its result.
func (a *Agent) observeCompaction(ctx context.Context, ok bool) {
	result := "ok"
	if !ok {
		result = "failed"
	}
	a.in.compactions.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
