package agent

import (
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

// call routes task and tries the candidates in order until one answers,
// each with req for its model. Any error moves to the next candidate
// (spec 7.3, layer 3); a cancelled ctx stops. Every attempt goes into the
// trace rec. ok is false when no candidate answered.
func (a *Agent) call(ctx context.Context, task router.TaskReq, req provider.ChatRequest, rec *turns.Turn) (provider.ChatResponse, bool) {
	cands, err := a.d.Router.Route(ctx, task)
	if err != nil {
		a.log.Error("agent: no model fits", "conv", rec.Conv, "kind", task.Kind, "err", err)
		return provider.ChatResponse{}, false
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			return provider.ChatResponse{}, false
		}
		p, ok := a.d.Providers[c.Provider]
		if !ok {
			rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: "no_provider"})
			continue
		}
		req.Model = c.Model
		resp, err := a.try(ctx, p, c, task, req)
		if err == nil {
			rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model})
			addUsage(rec, c, resp.Usage)
			return resp, true
		}
		rec.Route = append(rec.Route, turns.Attempt{Provider: c.Provider, Model: c.Model, Error: errorKind(err)})
		attrs := []any{"conv", rec.Conv, "provider", c.Provider, "model", c.Model, "kind", errorKind(err)}
		if pe, ok := errors.AsType[*provider.Error](err); ok {
			attrs = append(attrs, "status", pe.Status, "msg", pe.Msg) // provider text, keys redacted; never the prompt
		}
		a.log.Warn("agent: model call failed", attrs...)
	}
	return provider.ChatResponse{}, false
}

// try makes one call to candidate c through p under CallTimeout,
// observes it and reports its outcome to the router. An empty answer is an
// error.
func (a *Agent) try(ctx context.Context, p provider.Provider, c router.Candidate, task router.TaskReq, req provider.ChatRequest) (provider.ChatResponse, error) {
	spanCtx, span := a.in.tracer.Start(ctx, "model.call", trace.WithAttributes(
		attribute.String("provider", c.Provider), attribute.String("model", c.Model), attribute.String("task", string(task.Kind))))
	callCtx, cancel := context.WithTimeout(spanCtx, a.cfg.CallTimeout)
	started := time.Now()
	resp, err := p.Chat(callCtx, req)
	cancel()
	if err == nil && strings.TrimSpace(resp.Message.Content) == "" && len(resp.Message.ToolCalls) == 0 {
		err = &provider.Error{Provider: c.Provider, Kind: provider.KindUnavailable, Msg: "empty answer"}
	}
	a.observeCall(ctx, span, c, resp.Usage, err, time.Since(started))
	a.d.Router.Report(c, err)
	return resp, err
}

func errorKind(err error) string {
	if e, ok := errors.AsType[*provider.Error](err); ok {
		return string(e.Kind)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(provider.KindTimeout)
	}
	return "error"
}
