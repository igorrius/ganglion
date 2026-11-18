package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// fakeMCP is an MCPAdmin with one server and one button.
type fakeMCP struct{ asked []string }

func (f *fakeMCP) Card(_ context.Context, _ texts.Speaker, principal, conv string, _ *time.Location) (string, []event.Button, error) {
	f.asked = append(f.asked, principal+" "+conv)
	return "MCP servers:\n- `shop`: connected, tools: 3", []event.Button{{ID: "B1", Label: "Disconnect shop"}}, nil
}

// /mcp shows the card with its buttons in the admin's private chat, with no
// model and nothing in the history; anywhere else, or for anyone else, it
// shows nothing (R-MCP-10).
func TestMCPCommand(t *testing.T) {
	f := &fakeMCP{}
	e := newEnv(t, Config{}, nil)
	e.deps.MCP = f
	e.deps.Private = privateOf
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	out := e.turn(t, message(owner, "Igor", "/mcp"))
	if len(out) != 1 || !strings.Contains(out[0].Text, "`shop`: connected") || len(out[0].Buttons) != 1 || out[0].Buttons[0].ID != "B1" {
		t.Fatalf("/mcp = %+v", out)
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/mcp reached the model or the history")
	}
	ev := message(owner, "Igor", "/mcp")
	ev.Conv = "telegram:-100500"
	if out := e.turn(t, ev); len(out) != 1 || !strings.Contains(out[0].Text, "private chat") || len(out[0].Buttons) != 0 {
		t.Fatalf("in a group %+v", out)
	}
	e.admin = false
	if out := e.turn(t, message("telegram:user:7", "Olena", "/mcp")); len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("a family member got %+v", out)
	}
	if len(f.asked) != 1 {
		t.Fatalf("the card was asked %d times", len(f.asked))
	}
}

func TestNoMCPSaysNoneIsConnected(t *testing.T) {
	text, buttons, err := NoMCP{}.Card(context.Background(), texts.Speaker{}, "", "", time.UTC)
	if err != nil || len(buttons) != 0 || !strings.HasPrefix(text, "No MCP server") {
		t.Fatalf("NoMCP = %q %v %v", text, buttons, err)
	}
}
