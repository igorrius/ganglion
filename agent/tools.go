package agent

import (
	"context"
	"errors"
	"slices"
	"strconv"
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

// errCallLimit refuses a CLI provider's tool call once the turn's
// budget, MaxSteps calls, is spent (ADR-0036); the model reads it.
var errCallLimit = errors.New("tool call limit for this turn reached; answer with what you have")

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
// (the policy refuses the others anyway), an expiry's turn none (ruling
// R33), and the persona tool is offered
// only where the introduction rules are (a session with outside content
// is not asked for a persona; the tool refuses it anyway).
func (a *Agent) toolSpecs(t *turn) []provider.Tool {
	if t.noTools {
		return nil
	}
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
// as tool messages; a failed call's result is its error text. No call starts
// while an addition waits (R-LIVE-2): this one and the rest get a result
// saying so, are not numbered and are not journalled. run is how many ran,
// from the first.
func (a *Agent) runTools(ctx context.Context, t *turn, calls []provider.ToolCall) (out []provider.Message, run int) {
	out = make([]provider.Message, 0, len(calls))
	for i, call := range calls {
		if len(a.waiting(ctx, t)) > 0 {
			for _, skipped := range calls[i:] {
				out = append(out, provider.Message{Role: provider.RoleTool, ToolCallID: skipped.ID, Content: skippedResult})
			}
			a.in.skippedCalls.Add(ctx, int64(len(calls)-i))
			return out, i
		}
		out = append(out, a.runTool(ctx, t, call))
	}
	return out, len(calls)
}

// runTool runs one call of an API model's tool_calls and returns its
// result as a tool message; a failed call's result is its error text.
func (a *Agent) runTool(ctx context.Context, t *turn, call provider.ToolCall) provider.Message {
	text, err := a.useTool(ctx, t, call, false)
	if err != nil {
		text = "error: " + err.Error()
	}
	return provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: text}
}

// exec is the turn's provider.Exec for a provider that runs its own tool
// loop (the claude CLI over the MCP bridge, R-PRV-6): every call takes
// the same path as an API model's, through useTool, and the turn's CLI
// calls are capped at MaxSteps, the API loop's limit (ADR-0036). Each
// call that ran is logged for the history as an API model's call would
// be (takeExecLog).
func (a *Agent) exec(t *turn) provider.Exec {
	return func(ctx context.Context, call provider.ToolCall) (string, error) {
		return a.useTool(ctx, t, call, true)
	}
}

// logExec keeps a CLI provider's call and its result for the history, in
// the API path's form: an assistant message with the call and the tool
// message that answers it. The call has no id over MCP, so it gets one
// from the turn and its number, unique in the session. The caller holds
// t.mu.
func (t *turn) logExec(call provider.ToolCall, text string, err error) {
	call.ID = "cli_" + strings.ToLower(t.event.ID) + "_" + strconv.Itoa(t.calls)
	if err != nil {
		text = "error: " + err.Error()
	}
	t.execLog = append(t.execLog,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: text})
}

// takeExecLog returns the calls a CLI provider ran since the last take,
// for the history, and forgets them.
func (t *turn) takeExecLog() []provider.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.execLog
	t.execLog = nil
	return out
}

// toolCalls is the number of calls the turn has run; a callCount.
func (t *turn) toolCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
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
// turn's count. A capped call (a CLI provider's) past the turn's budget
// is refused with errCallLimit before it is numbered, run or journalled.
func (a *Agent) useTool(ctx context.Context, t *turn, call provider.ToolCall, capped bool) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if capped {
		if t.execCalls >= a.cfg.MaxSteps {
			return "", errCallLimit
		}
		t.execCalls++
	}
	t.calls++
	text, err := a.applyTool(ctx, t, call)
	if capped {
		t.logExec(call, text, err)
	}
	return text, err
}

// applyTool runs the turn's numbered call through the chain and applies
// its result to the turn (useTool). The caller holds t.mu.
func (a *Agent) applyTool(ctx context.Context, t *turn, call provider.ToolCall) (string, error) {
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
		Admin: t.admin, Tainted: t.state.Tainted, LocalOnly: t.state.LocalOnly, Grant: t.grant, KnownURL: t.known.has, Depth: t.depth,
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
// by id. A note (confirmation, button, command) is not the person's
// words and does not count, nor does outside text in a message (a forward, a quote).
func knownURLs(msgs []provider.Message) urlSet {
	set := urlSet{}
	searches := map[string]bool{} // ids of web.search calls
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			if !isNote(m.Content) {
				set.add(tool.StripExternal(m.Content))
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

// isNote reports whether a user-role message is a note the agent wrote
// into the history, not the person's words.
func isNote(content string) bool {
	for _, p := range []string{confirmationPrefix, buttonPrefix, commandPrefix} {
		if strings.HasPrefix(content, p) {
			return true
		}
	}
	return false
}
