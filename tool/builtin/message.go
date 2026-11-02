// Package builtin holds the first real tools of the agent: message.send
// and the cells tools. Each takes what it needs as a small interface
// declared here; config wires the real ones (ADR-0028).
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/tool"
)

// ErrUnknownConv is a message to a conversation the node does not know.
var ErrUnknownConv = errors.New("builtin: unknown conversation")

// States reads a conversation's stored state (agent.StateReader). A
// conversation the node has talked in has a non-empty one.
type States interface {
	State(ctx context.Context, conv string) (json.RawMessage, error)
}

// ListedFunc reports whether a principal is on the access list
// (access.List.Listed).
type ListedFunc func(ctx context.Context, principal string) (bool, error)

// Convs tells which conversations a message may go to: the one the call
// comes from, one the node has state for, or the private conversation of
// a principal on the access list.
type Convs struct {
	States  States
	Listed  ListedFunc
	Private alert.PrivateConvFunc // principal -> private conversation (telegram.PrivateConv)
}

// resolve returns where a message to target goes. target is a
// conversation key or a principal such as "telegram:user:7"; a known
// conversation is reached on the transport of the call.
func (v Convs) resolve(ctx context.Context, c tool.Call, target string) (transport, conv string, err error) {
	switch target {
	case "":
		return "", "", fmt.Errorf("%w: no conv given", ErrUnknownConv)
	case c.Conv:
		return c.Transport, c.Conv, nil
	}
	if tr, cv, ok := v.Private(target); ok {
		listed, err := v.Listed(ctx, target)
		if err != nil {
			return "", "", fmt.Errorf("builtin: access list: %w", err)
		}
		if !listed {
			return "", "", fmt.Errorf("%w %q: the principal is not on the access list", ErrUnknownConv, target)
		}
		return tr, cv, nil
	}
	state, err := v.States.State(ctx, target)
	if err != nil {
		return "", "", fmt.Errorf("builtin: state of %s: %w", target, err)
	}
	if len(state) == 0 {
		return "", "", fmt.Errorf("%w %q: the node has no conversation by that key", ErrUnknownConv, target)
	}
	return c.Transport, target, nil
}

// MessageSend is the message.send tool: a message to a conversation or a
// person, delivered through the outbox with the turn's replies.
type MessageSend struct {
	convs Convs
}

// NewMessageSend returns the message.send tool over convs.
func NewMessageSend(convs Convs) *MessageSend { return &MessageSend{convs: convs} }

// Spec describes message.send. The policy confirms every call: it is a
// side effect.
func (*MessageSend) Spec() tool.Spec {
	return tool.Spec{
		Name: "message.send",
		Description: "Send a message to a conversation the assistant knows or to a person on the access list. " +
			"conv is a conversation key or a person's id such as telegram:user:123456. " +
			"The owner confirms the message before it goes out.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"conv":{"type":"string","description":"conversation key, or the person's id such as telegram:user:123456"},` +
			`"text":{"type":"string","description":"the message text"}},` +
			`"required":["conv","text"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

type messageArgs struct {
	Conv string `json:"conv"`
	Text string `json:"text"`
}

// Run validates the arguments and the target, and only then builds the
// message: a call that errs carries none (tool.Result.Out is delivered
// even on an error). The message's idempotency key is the call's key plus
// ":send", so a rerun turn sends it once (R-DLV-6).
func (m *MessageSend) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a messageArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("message.send: arguments: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return tool.Result{}, errors.New("message.send: text is empty")
	}
	transport, conv, err := m.convs.resolve(ctx, c, a.Conv)
	if err != nil {
		return tool.Result{}, fmt.Errorf("message.send: %w", err)
	}
	return tool.Result{
		Text: "message queued for " + conv,
		Out:  []event.Outgoing{{IdemKey: c.Key() + ":send", Transport: transport, Conv: conv, Text: a.Text}},
	}, nil
}
