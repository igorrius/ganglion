package agent

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// callCount reports the tool calls a turn has run so far; a CLI provider
// runs them during an attempt, through provider.Exec.
type callCount func() int

// noCalls is the callCount of a model call without tools (compaction).
func noCalls() int { return 0 }

// call routes task and tries the candidates in order until one answers,
// each with req for its model. Any error moves to the next candidate
// (spec 7.3, layer 3); a cancelled ctx stops. Every attempt goes into the
// trace rec. The outcome is OutcomeOK when a candidate answered,
// OutcomeNoModel when none did, and OutcomeError when an attempt failed
// after it ran tools (ran counts them): the next model would not know
// those calls ran, so there is no fallback then (ADR-0036). A run ended for an
// addition is outcomeEnded (R-LIVE-3): the core sent what it estimates, since
// such a run reports no usage, and made one model call per tool call plus one.
func (a *Agent) call(ctx context.Context, task router.TaskReq, req provider.ChatRequest, rec *turns.Turn, ran callCount) (provider.ChatResponse, turns.Outcome) {
	cands, err := a.d.Router.Route(ctx, task)
	if err != nil {
		a.log.Error("agent: no model fits", "conv", rec.Conv, "kind", string(task.Purpose), "err", err)
		return provider.ChatResponse{}, turns.OutcomeNoModel
	}
	firstFail := "" // the first candidate's error kind, for a fallback
	for _, c := range cands {
		if ctx.Err() != nil {
			return provider.ChatResponse{}, turns.OutcomeNoModel
		}
		p, ok := a.d.Providers[c.Provider]
		if !ok {
			rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: "no_provider"})
			firstFail = cmp.Or(firstFail, "no_provider")
			continue
		}
		req.Model, req.Effort = c.Model, c.Effort
		before := ran()
		resp, err := a.try(ctx, p, c, task, req)
		if errors.Is(err, errAdditionWaiting) {
			rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: addedKind})
			est := provider.Usage{InputTokens: estimateTokens(req.Messages) * (ran() - before + 1)}
			addUsage(rec, c, est)
			a.observeRunEnded(ctx, c, est)
			return provider.ChatResponse{}, outcomeEnded
		}
		if err == nil {
			rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model})
			addUsage(rec, c, resp.Usage)
			if firstFail != "" {
				a.observeFallback(ctx, firstFail)
			}
			return resp, turns.OutcomeOK
		}
		kind := errorKind(err)
		rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: kind})
		firstFail = cmp.Or(firstFail, kind)
		a.logFailure(rec, c, err)
		if calls := ran() - before; calls > 0 {
			rec.Error = "the model failed after running tools; no fallback"
			a.log.Warn("agent: no fallback after a failed run with tools", "conv", rec.Conv, "provider", c.Provider, "calls", calls)
			return provider.ChatResponse{}, turns.OutcomeError
		}
	}
	return provider.ChatResponse{}, turns.OutcomeNoModel
}

// addedKind is the error kind of a run ended for an addition, in the trace
// and on the calls metric. It is not a failure.
const addedKind = turns.EndedForAddition

// logFailure logs a failed model call.
func (a *Agent) logFailure(rec *turns.Turn, c router.Candidate, err error) {
	attrs := []any{"conv", rec.Conv, "provider", c.Provider, "model", c.Model, "kind", errorKind(err)}
	if pe, ok := errors.AsType[*provider.Error](err); ok {
		attrs = append(attrs, "status", pe.Status, "msg", pe.Msg) // provider text, keys redacted; never the prompt
	}
	a.log.Warn("agent: model call failed", attrs...)
}

// try makes one call to candidate c through p under CallTimeout,
// observes it and reports its outcome to the router. An empty answer is an
// error.
func (a *Agent) try(ctx context.Context, p provider.Provider, c router.Candidate, task router.TaskReq, req provider.ChatRequest) (provider.ChatResponse, error) {
	spanCtx, span := a.in.tracer.Start(ctx, "model.call", trace.WithAttributes(
		attribute.String("provider", c.Provider), attribute.String("model", c.Model), attribute.String("task", string(task.Purpose))))
	callCtx, cancel := context.WithTimeout(spanCtx, a.cfg.CallTimeout)
	started := time.Now()
	resp, err := p.Chat(callCtx, req)
	cancel()
	if err != nil && errors.Is(context.Cause(ctx), errAdditionWaiting) {
		err = errAdditionWaiting
	}
	if err == nil && strings.TrimSpace(resp.Message.Content) == "" && len(resp.Message.ToolCalls) == 0 {
		err = &provider.Error{Provider: c.Provider, Kind: provider.KindUnavailable, Msg: "empty answer"}
	}
	a.observeCall(ctx, span, c, resp.Usage, err, time.Since(started))
	a.d.Router.Report(c, err)
	return resp, err
}

func errorKind(err error) string {
	if errors.Is(err, errAdditionWaiting) {
		return addedKind
	}
	if e, ok := errors.AsType[*provider.Error](err); ok {
		return string(e.Kind)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(provider.KindTimeout)
	}
	return "error"
}
