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
	"github.com/igorrius/ganglion/texts"
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

// NameFunc returns the name a principal is listed under (access.List.Name);
// "" when it has none.
type NameFunc func(ctx context.Context, principal string) (string, error)

// Convs tells which conversations a message may go to: the one the call
// comes from, one the node has state for, or the private conversation of
// a principal on the access list.
type Convs struct {
	States  States
	Listed  ListedFunc
	Private alert.PrivateConvFunc // principal -> private conversation (telegram.PrivateConv)
	// Dests resolves a named destination (groups.Destinations.Resolve);
	// unset knows none.
	Dests DestinationFunc
	Names NameFunc // who a principal is, for the owner's question
}

// DestinationFunc resolves a destination's name to its transport and
// conversation; ok is false when there is none by that name.
type DestinationFunc func(name string) (transport, conv string, ok bool, err error)

// destination resolves target as a destination's name.
func (v Convs) destination(target string) (transport, conv string, ok bool, err error) {
	if v.Dests == nil {
		return "", "", false, nil
	}
	return v.Dests(target)
}

// resolve returns where a message to target goes. target is a
// conversation key, a principal such as "telegram:user:7", or the name of
// a destination the admin defined (R-CFG-12); a known conversation is
// reached on the transport of the call.
// A scheduled task's own conversation ("task:<id>") is no chat: a message
// to it goes to the task's reply target.
func (v Convs) resolve(ctx context.Context, c tool.Call, target string) (transport, conv string, err error) {
	switch target {
	case "":
		return "", "", fmt.Errorf("%w: no conv given", ErrUnknownConv)
	case c.Conv:
		if c.Grant != nil {
			return c.Transport, c.Grant.ReplyTo, nil
		}
		return c.Transport, c.Conv, nil
	}
	if tr, cv, ok, err := v.destination(target); err != nil {
		return "", "", fmt.Errorf("builtin: destinations: %w", err)
	} else if ok {
		return tr, cv, nil
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
		Description: "Send a message to a named destination, a conversation the assistant knows or a person on the access list. " +
			"conv is a destination's name from destination.list (such as family-k7), a conversation key, or a person's id such as telegram:user:123456. " +
			"The owner confirms the message before it goes out.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"conv":{"type":"string","description":"a destination's name, a conversation key, or the person's id such as telegram:user:123456"},` +
			`"text":{"type":"string","description":"the message text"}},` +
			`"required":["conv","text"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

// MaxMessageRunes bounds a message's text: the owner reads all of it in
// the confirmation question, which the policy cuts at
// tool.MaxQuestionRunes, so the bound leaves room for the words around it
// (ruling R34).
const MaxMessageRunes = 2500

type messageArgs struct {
	Conv string `json:"conv"`
	Text string `json:"text"`
}

// args reads and checks a call's arguments: a text the question can show
// whole (ruling R34).
func (*MessageSend) args(c tool.Call) (messageArgs, error) {
	var a messageArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return messageArgs{}, fmt.Errorf("message.send: arguments: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return messageArgs{}, errors.New("message.send: text is empty")
	}
	if err := showable("message.send", a.Text, MaxMessageRunes); err != nil {
		return messageArgs{}, err
	}
	return a, nil
}

// Question implements tool.Questioner: the recipient, by name where the
// access list has one, and the whole text, as it would go out (ruling
// R34). An unknown recipient is refused without a question.
func (m *MessageSend) Question(ctx context.Context, c tool.Call) (string, error) {
	a, err := m.args(c)
	if err != nil {
		return "", err
	}
	_, conv, err := m.convs.resolve(ctx, c, a.Conv)
	if err != nil {
		return "", fmt.Errorf("message.send: %w", err)
	}
	to, err := m.recipient(ctx, c, a.Conv, conv)
	if err != nil {
		return "", err
	}
	return whole("message.send", c.Speak.Text(texts.AskSendMessage, texts.Vars{"to": to, varText: a.Text}))
}

// recipient names where a message to target goes (conv, resolved): this
// chat, a person by their listed name and id, or another conversation by
// its key.
func (m *MessageSend) recipient(ctx context.Context, c tool.Call, target, conv string) (string, error) {
	if target == c.Conv && conv == c.Conv {
		return "this chat", nil
	}
	if _, _, ok, err := m.convs.destination(target); err == nil && ok {
		return "the destination " + oneLine(target) + " (" + oneLine(conv) + ")", nil
	}
	if _, _, ok := m.convs.Private(target); !ok {
		return "the conversation " + oneLine(conv), nil
	}
	name, err := m.convs.Names(ctx, target)
	if err != nil {
		return "", fmt.Errorf("message.send: name of %s: %w", target, err)
	}
	if name = oneLine(name); name == "" {
		return oneLine(target), nil
	}
	return name + " (" + oneLine(target) + ")", nil
}

// Run validates the arguments and the target, and only then builds the
// message: a call that errs carries none (tool.Result.Out is delivered
// even on an error). The message's idempotency key is the call's key plus
// ":send", so a rerun turn sends it once (R-DLV-6).
func (m *MessageSend) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	a, err := m.args(c)
	if err != nil {
		return tool.Result{}, err
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
