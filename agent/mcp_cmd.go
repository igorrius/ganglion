package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport"
)

// MCPAdmin is what /mcp shows (mcpadmin.Admin.Card): each remote MCP
// server with its state, last sign-in and tools, and a Disconnect button
// for each, stored for principal in conv.
type MCPAdmin interface {
	Card(ctx context.Context, sp texts.Speaker, principal, conv string, loc *time.Location) (string, []event.Button, error)
}

// NoMCP is a node without remote MCP servers.
type NoMCP struct{}

// Card says no server is connected.
func (NoMCP) Card(_ context.Context, sp texts.Speaker, _, _ string, _ *time.Location) (string, []event.Button, error) {
	return sp.Text(texts.MCPListNone, nil), nil, nil
}

// mcpHint is /mcp's description in the command hints.
func mcpHint() map[transport.Lang]string {
	return map[transport.Lang]string{
		transport.LangDefault:   "Remote MCP servers: their state, and Disconnect",
		transport.LangRussian:   "Удалённые MCP-серверы: состояние и отключение",
		transport.LangUkrainian: "Віддалені MCP-сервери: стан і відключення"}
}

// mcpCmd is /mcp (R-MCP-10): the remote MCP servers, for an admin in their
// private chat, without a model and with nothing in the history. Its
// Disconnect buttons are quick actions, whose press notes the change.
func (a *Agent) mcpCmd(ctx context.Context, t *turn, _ string) error {
	return a.cardCmd(ctx, t, "/mcp", texts.MCPPrivateOnly, a.d.MCP.Card)
}

// cardFunc makes the card of a command: its text and buttons for principal
// in conv.
type cardFunc func(ctx context.Context, sp texts.Speaker, principal, conv string, loc *time.Location) (string, []event.Button, error)

// cardCmd answers a command that shows an admin's card in their private
// chat: nobody else gets it, and nowhere else is it shown (privateOnly says
// so). No model is asked and nothing enters the history.
func (a *Agent) cardCmd(ctx context.Context, t *turn, name string, privateOnly texts.Key, card cardFunc) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	if _, conv, ok := a.d.Private(t.msg.From); !ok || conv != t.event.Conv {
		t.reply(a.say(ctx, t, privateOnly, nil))
		return nil
	}
	text, buttons, err := card(ctx, a.speaker(ctx, t), t.msg.From, t.event.Conv, a.cfg.Location)
	if err != nil {
		return fmt.Errorf("agent: %s: %w", name, err)
	}
	t.reply(text)
	if len(buttons) > 0 {
		t.out[len(t.out)-1].Buttons = buttons
	}
	return nil
}
