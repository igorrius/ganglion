package agent

import (
	"context"
	"slices"
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

// proposeTool is the tool that ends the first-run introduction.
const proposeTool = "persona.propose"

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
// provider's shape; a scheduled task is offered only its grant's tools
// (the policy refuses the others anyway), and the persona tool is offered
// only where the introduction rules are (a session with outside content
// is not asked for a persona; the tool refuses it anyway).
func (a *Agent) toolSpecs(t *turn) []provider.Tool {
	specs := a.d.Tools.Specs(t.admin)
	out := make([]provider.Tool, 0, len(specs))
	for _, s := range specs {
		if s.Name == proposeTool && !a.introducing(t) {
			continue
		}
		if t.grant == nil || slices.Contains(t.grant.Tools, s.Name) {
			out = append(out, provider.Tool{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema})
		}
	}
	return out
}

// introducing reports whether the turn gets the introduction rules: an
// admin's session that holds no outside content, outside a scheduled
// task, while the persona tool is on offer, which the registry lists only
// until persona/ has a *.md file.
func (a *Agent) introducing(t *turn) bool {
	return t.admin && !t.state.Tainted && t.grant == nil && a.d.Tools.Offered(proposeTool, true)
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

// runTool runs one call of an API model's tool_calls and returns its
// result as a tool message; a failed call's result is its error text.
func (a *Agent) runTool(ctx context.Context, t *turn, call provider.ToolCall) provider.Message {
	text, err := a.useTool(ctx, t, call)
	if err != nil {
		text = "error: " + err.Error()
	}
	return provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: text}
}

// exec is the turn's provider.Exec for a provider that runs its own tool
// loop (the claude CLI over the MCP bridge, R-PRV-6): every call takes
// the same path as an API model's, through useTool.
func (a *Agent) exec(t *turn) provider.Exec {
	return func(ctx context.Context, call provider.ToolCall) (string, error) {
		return a.useTool(ctx, t, call)
	}
}

// useTool runs one call and applies its result to the turn: it numbers
// the call, runs it through the chain, marks an external result as data
// and taints the session, keeps the tool's outgoing messages for the
// turn, whether the call failed or not, and remembers a call parked for
// confirmation in the state. It returns the text the model reads.
//
// It holds the turn's lock for the whole call: a CLI provider may call
// in parallel, and a later call must see the taint an earlier external
// result set (ADR-0036), with the call numbers continuing the
// turn's count.
func (a *Agent) useTool(ctx context.Context, t *turn, call provider.ToolCall) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	started := time.Now()
	result, err := a.tool(ctx, a.toolCall(t, call))
	// A failed call can still carry messages, such as the alert about
	// a call outside a task's grant.
	t.out = append(t.out, result.Out...)
	t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: call.Name, OK: err == nil, Ms: time.Since(started).Milliseconds()})
	if err != nil {
		return "", err
	}
	text := result.Text
	if result.External {
		t.state.Tainted = true
		text = tool.WrapExternal(call.Name, result.Text)
	}
	if call.Name == searchTool {
		t.known.add(result.Text)
	}
	if result.Pending != "" && !slices.Contains(t.state.Pending, result.Pending) {
		t.state.Pending = append(t.state.Pending, result.Pending)
	}
	return text, nil
}

// toolCall is the context of the turn's next call.
func (a *Agent) toolCall(t *turn, call provider.ToolCall) tool.Call {
	return tool.Call{
		TurnID: t.event.ID, N: t.calls, Conv: t.event.Conv, Transport: t.transport, Principal: t.msg.From,
		Admin: t.admin, Tainted: t.state.Tainted, Grant: t.grant, KnownURL: t.known.has,
		Name: call.Name, Args: call.Args,
	}
}

// urlSet is the normalised addresses a turn may fetch without asking: the
// ones in the user's messages and in earlier web.search results.
type urlSet map[string]struct{}

// add puts the addresses of text in the set.
func (s urlSet) add(text string) {
	for _, raw := range urlnorm.Find(text) {
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

// knownURLs collects the set from a session's history: the person's
// messages and the results of web.search calls, paired with their calls
// by id. A confirmation note is not the person's words and does not count.
func knownURLs(msgs []provider.Message) urlSet {
	set := urlSet{}
	searches := map[string]bool{} // ids of web.search calls
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			if !strings.HasPrefix(m.Content, confirmationPrefix) {
				set.add(m.Content)
			}
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
