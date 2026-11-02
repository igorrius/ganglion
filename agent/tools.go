package agent

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/urlnorm"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// AdminFunc reports whether a principal is an admin: only admins get
// tools (design section 4, rule 8). An error leaves the turn to be
// handled again; it never counts as "not an admin".
type AdminFunc func(ctx context.Context, principal string) (bool, error)

// searchTool is the tool whose results make URLs known (design section 4,
// rule 2).
const searchTool = "web.search"

// traced puts every tool call in a span. Nothing from the arguments or
// the result goes into it.
func (a *Agent) traced(next tool.Func) tool.Func {
	return func(ctx context.Context, c tool.Call) (tool.Result, error) {
		ctx, span := a.in.tracer.Start(ctx, "tool.call", trace.WithAttributes(attribute.String("tool", c.Name)))
		defer span.End()
		result, err := next(ctx, c)
		span.SetAttributes(attribute.Bool("ok", err == nil))
		return result, err
	}
}

// toolSpecs are the tools the model is offered in this turn, in the
// provider's shape.
func (a *Agent) toolSpecs(t *turn) []provider.Tool {
	specs := a.d.Tools.Specs(t.admin)
	out := make([]provider.Tool, len(specs))
	for i, s := range specs {
		out[i] = provider.Tool{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema}
	}
	return out
}

// runTools runs the model's tool calls in order and returns their results
// as tool messages; a failed call's result is its error text.
func (a *Agent) runTools(ctx context.Context, t *turn, calls []provider.ToolCall) []provider.Message {
	out := make([]provider.Message, 0, len(calls))
	for _, call := range calls {
		out = append(out, a.runTool(ctx, t, call))
	}
	return out
}

// runTool runs one call: it numbers it, runs it through the chain, marks
// an external result as data and taints the session, and keeps the tool's
// outgoing messages for the turn, whether the call failed or not.
func (a *Agent) runTool(ctx context.Context, t *turn, call provider.ToolCall) provider.Message {
	t.calls++
	started := time.Now()
	result, err := a.tool(ctx, a.toolCall(t, call))
	text := result.Text
	// A call that failed can still carry messages, such as the alert about
	// a call outside a task's grant.
	t.out = append(t.out, result.Out...)
	switch {
	case err != nil:
		text = "error: " + err.Error()
	default:
		if result.External {
			t.state.Tainted = true
			text = tool.WrapExternal(call.Name, result.Text)
		}
		if call.Name == searchTool {
			t.known.add(result.Text)
		}
	}
	t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: call.Name, OK: err == nil, Ms: time.Since(started).Milliseconds()})
	return provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: text}
}

// toolCall is the context of the turn's next call.
func (a *Agent) toolCall(t *turn, call provider.ToolCall) tool.Call {
	return tool.Call{
		TurnID: t.event.ID, N: t.calls, Conv: t.event.Conv, Principal: t.msg.From,
		Admin: t.admin, Tainted: t.state.Tainted, KnownURL: t.known.has,
		Name: call.Name, Args: call.Args,
	}
}

// urlPattern finds an http(s) address in text: it runs up to whitespace
// or a closing ")", ">" or quote.
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s)>"]+`)

// extractURLs returns the addresses in text; punctuation that ends a
// sentence after an address is not part of it.
func extractURLs(text string) []string {
	found := urlPattern.FindAllString(text, -1)
	for i, u := range found {
		found[i] = strings.TrimRight(u, ".,;:!?")
	}
	return found
}

// urlSet is the normalised addresses a turn may fetch without asking: the
// ones in the user's messages and in earlier web.search results.
type urlSet map[string]struct{}

// add puts the addresses of text in the set.
func (s urlSet) add(text string) {
	for _, raw := range extractURLs(text) {
		if u, ok := urlnorm.Normalize(raw); ok {
			s[u] = struct{}{}
		}
	}
}

// has reports whether raw, normalised, is in the set.
func (s urlSet) has(raw string) bool {
	u, ok := urlnorm.Normalize(raw)
	if !ok {
		return false
	}
	_, known := s[u]
	return known
}

// knownURLs collects the set from a session's history: user messages and
// the results of web.search calls, paired with their calls by id.
func knownURLs(msgs []provider.Message) urlSet {
	set := urlSet{}
	searches := map[string]bool{} // ids of web.search calls
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			set.add(m.Content)
		case provider.RoleAssistant:
			for _, c := range m.ToolCalls {
				searches[c.ID] = c.Name == searchTool
			}
		case provider.RoleTool:
			if searches[m.ToolCallID] {
				set.add(m.Content)
			}
		case provider.RoleSystem:
		}
	}
	return set
}
