package agent

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/turns"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Tools is the registry the agent offers to the model.
type Tools interface {
	List() []provider.Tool
	Call(ctx context.Context, call provider.ToolCall) (string, error)
}

// NoTools is an empty registry (M3a).
type NoTools struct{}

// List implements Tools.
func (NoTools) List() []provider.Tool { return nil }

// Call implements Tools.
func (NoTools) Call(context.Context, provider.ToolCall) (string, error) {
	return "", errors.New("no tools")
}

// ToolFunc runs one tool call and returns its result for the model.
type ToolFunc func(ctx context.Context, call provider.ToolCall) (string, error)

// ToolMiddleware wraps a ToolFunc with behaviour around every call:
// tracing now, policies and the depth limit in M3b (spec principle 6).
type ToolMiddleware func(next ToolFunc) ToolFunc

// chain wraps f so that mw[0] runs first.
func chain(f ToolFunc, mw ...ToolMiddleware) ToolFunc {
	for _, m := range slices.Backward(mw) {
		f = m(f)
	}
	return f
}

// traced puts every tool call in a span. Nothing from the arguments or
// the result goes into it.
func (a *Agent) traced(next ToolFunc) ToolFunc {
	return func(ctx context.Context, call provider.ToolCall) (string, error) {
		ctx, span := a.in.tracer.Start(ctx, "tool.call", trace.WithAttributes(attribute.String("tool", call.Name)))
		defer span.End()
		result, err := next(ctx, call)
		span.SetAttributes(attribute.Bool("ok", err == nil))
		return result, err
	}
}

// runTools runs the model's tool calls in order and returns their results
// as tool messages; a failed call's result is its error text.
func (a *Agent) runTools(ctx context.Context, t *turn, calls []provider.ToolCall) []provider.Message {
	out := make([]provider.Message, 0, len(calls))
	for _, call := range calls {
		started := time.Now()
		result, err := a.tool(ctx, call)
		if err != nil {
			result = "error: " + err.Error()
		}
		t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: call.Name, OK: err == nil, Ms: time.Since(started).Milliseconds()})
		out = append(out, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: result})
	}
	return out
}
