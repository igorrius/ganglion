package mcpclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/tool"
)

// refusedPrefix and the like are what the model reads of a result the server
// wrote: always outside content, so the agent wraps it as data (R-MCP-4).
const (
	toolErrorPrefix = "the tool reported an error: "
	refusedPrefix   = "the server refused the call: "
)

// remoteTool is one tool of a server, offered on demand (R-MCP-4a).
type remoteTool struct {
	hub    *Hub
	server string
	tool   Tool
	spec   tool.Spec
}

// Spec implements tool.Handler: admin-only, outside content, confirmed in a
// tainted session when it has input, never offered in a local-only session,
// and on demand (R-MCP-4).
func (t remoteTool) Spec() tool.Spec { return t.spec }

// Run implements tool.Handler: the call, then its result as outside content.
func (t remoteTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	return t.hub.call(ctx, t.server, t.tool, c)
}

// handlersOf are the tools to offer from the approved copy: the
// `forbidden` ones are never on offer (R-MCP-4a).
func (s *serverConn) handlersOf(tools []Tool) []tool.Handler {
	var out []tool.Handler
	for _, t := range tools {
		policy := s.cfg.Policies[t.Short]
		if policy == tool.PolicyForbidden {
			continue
		}
		class := tool.ClassSideEffect
		if t.ReadOnly {
			class = tool.ClassExternal
		}
		out = append(out, remoteTool{hub: s.hub, server: s.cfg.Name, tool: t, spec: tool.Spec{
			Name: t.Name, Description: describe(s.cfg.Name, t), InputSchema: t.InputSchema,
			Class: class, Policy: policy, AdminOnly: true, AskTaintedInput: true, OnDemand: true,
		}})
	}
	return out
}

// describe is the description the model reads for a tool.
func describe(server string, t Tool) string {
	switch {
	case t.Description != "":
		return t.Description
	case t.Title != "":
		return t.Title
	}
	return "A tool of the remote server " + server + "."
}

// nodeError is a failure in the node's own words, which the model reads and
// the journal records; kind lets a caller decide with errors.Is.
type nodeError struct {
	text string
	kind error
}

func (e *nodeError) Error() string { return e.text }
func (e *nodeError) Unwrap() error { return e.kind }

// call runs the call of a server's tool and counts it.
func (h *Hub) call(ctx context.Context, server string, t Tool, c tool.Call) (tool.Result, error) {
	started := h.now()
	s, ok := h.conn(server)
	if !ok {
		h.in.call(ctx, server, outcomeRefused, h.now().Sub(started))
		return tool.Result{}, &nodeError{text: server + " is not configured", kind: ErrUnreachable}
	}
	res, outcome, err := s.invoke(ctx, t, c)
	h.in.call(ctx, server, outcome, h.now().Sub(started))
	return res, err
}

// unavailableText is why the server cannot be called, in the node's words;
// ok is false when it can be.
func (s *serverConn) unavailableText() (text string, ok bool) {
	st, _ := s.snapshot()
	switch st {
	case StateConnected:
		return "", false
	case StateConnecting:
		return s.cfg.Name + " is still connecting; try again shortly", true
	case StateToolsChanged:
		return s.cfg.Name + "'s tool list changed; the admin must approve it again", true
	case StateUnauthorized:
		return s.cfg.Name + " needs a new sign-in", true
	case StateUnreachable:
	}
	return s.cfg.Name + " is unreachable; try again later", true
}

// invoke calls tool t of the server. Only a connected server is called. A
// call is never repeated by the node (R-MCP-8): the client's one repeat
// after a dropped session did not run it.
func (s *serverConn) invoke(ctx context.Context, t Tool, c tool.Call) (tool.Result, callOutcome, error) {
	if text, down := s.unavailableText(); down {
		return tool.Result{}, outcomeRefused, &nodeError{text: text, kind: ErrUnreachable}
	}
	r, err := s.client.CallTool(ctx, t.Own, c.Args)
	if err != nil {
		return s.settle(ctx, t, c, err)
	}
	if r.IsError {
		return tool.Result{Text: toolErrorPrefix + r.Text, External: true}, outcomeToolError, nil
	}
	return tool.Result{Text: r.Text, External: true}, outcomeOK, nil
}

// settle settles a call that returned an error.
func (s *serverConn) settle(ctx context.Context, t Tool, c tool.Call, err error) (tool.Result, callOutcome, error) {
	var rpc *RPCError
	var status *StatusError
	switch {
	case errors.As(err, &rpc):
		// The server's words are outside content, not the node's error.
		return tool.Result{Text: fmt.Sprintf("%s%d %s", refusedPrefix, rpc.Code, rpc.Message), External: true}, outcomeToolError, nil
	case errors.Is(err, ErrTooLarge):
		return tool.Result{}, outcomeTooLarge, &nodeError{text: tooLargeText(t, c.Name), kind: ErrTooLarge}
	case errors.Is(err, ErrUnauthorized):
		s.callFailed(StateUnauthorized)
		return tool.Result{}, outcomeRefused, &nodeError{text: s.cfg.Name + " needs a new sign-in", kind: ErrUnauthorized}
	case errors.Is(err, ErrBadArguments):
		return tool.Result{}, outcomeRefused, &nodeError{text: c.Name + ": the arguments must be a JSON object", kind: ErrBadArguments}
	case errors.As(err, &status) && status.Code < 500, errors.Is(err, ErrForbidden):
		// The server turned the call away before running it.
		return tool.Result{}, outcomeRefused, &nodeError{text: c.Name + ": " + s.cfg.Name + " refused the call; it was not run", kind: ErrForbidden}
	}
	return s.noAnswer(ctx, t, c, err)
}

// tooLargeText is the error of an answer over MaxCallBytes: the call ran.
func tooLargeText(t Tool, name string) string {
	msg := name + ": the result is over 4 MB and was not read"
	if !t.ReadOnly {
		msg += "; the call itself ran on the server"
	}
	return msg
}

// noAnswer settles a call that got no usable answer. A request that never
// left, or any read, is "unreachable"; a call that is not read-only may
// have run, and is "outcome unknown" (R-MCP-8). The server goes unreachable
// and is reconnected by the back-off; the node never repeats the call. A
// call cut by its own context blames nobody.
func (s *serverConn) noAnswer(ctx context.Context, t Tool, c tool.Call, err error) (tool.Result, callOutcome, error) {
	if ctx.Err() == nil {
		s.callFailed(StateUnreachable)
	}
	if NotSent(err) || t.ReadOnly {
		return tool.Result{}, outcomeUnreachable, &nodeError{text: s.cfg.Name + " is unreachable; try again later", kind: ErrUnreachable}
	}
	return tool.Result{}, outcomeUnknown, &nodeError{kind: ErrOutcomeUnknown,
		text: c.Name + ": outcome unknown: " + s.cfg.Name + " did not answer; the call may have run and was not repeated"}
}

// callFailed takes the server's tools off offer and wakes the watch, which
// reconnects after the back-off.
func (s *serverConn) callFailed(st State) {
	s.unavailable(st)
	s.signalFailed()
}
