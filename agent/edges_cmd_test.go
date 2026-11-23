package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// fakeEdges is an EdgesAdmin with one edge and one button.
type fakeEdges struct{ asked []string }

func (f *fakeEdges) Card(_ context.Context, _ texts.Speaker, principal, conv string, _ *time.Location) (string, []event.Button, error) {
	f.asked = append(f.asked, principal+" "+conv)
	return "Connected edges:\n- `home`: joined 20 Nov 09:05", []event.Button{{ID: "R1", Label: "Revoke home"}}, nil
}

// /edges shows the card with its buttons in the admin's private chat, with no
// model and nothing in the history; anywhere else, or for anyone else, it
// shows nothing.
func TestEdgesCommand(t *testing.T) {
	f := &fakeEdges{}
	e := newEnv(t, Config{}, nil)
	e.deps.Edges = f
	e.deps.Private = privateOf
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	out := e.turn(t, message(owner, "Igor", "/edges"))
	if len(out) != 1 || !strings.Contains(out[0].Text, "`home`") || len(out[0].Buttons) != 1 || out[0].Buttons[0].ID != "R1" {
		t.Fatalf("/edges = %+v", out)
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/edges reached the model or the history")
	}
	ev := message(owner, "Igor", "/edges")
	ev.Conv = "telegram:-100500"
	if out := e.turn(t, ev); len(out) != 1 || !strings.Contains(out[0].Text, "private chat") || len(out[0].Buttons) != 0 {
		t.Fatalf("in a group %+v", out)
	}
	e.admin = false
	if out := e.turn(t, message("telegram:user:7", "Olena", "/edges")); len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("a family member got %+v", out)
	}
	if len(f.asked) != 1 {
		t.Fatalf("the card was asked %d times", len(f.asked))
	}
}

func TestNoEdgesSaysNoneIsConnected(t *testing.T) {
	text, buttons, err := NoEdges{}.Card(context.Background(), texts.Speaker{}, "", "", time.UTC)
	if err != nil || len(buttons) != 0 || !strings.HasPrefix(text, "No computer") {
		t.Fatalf("NoEdges = %q %v %v", text, buttons, err)
	}
}
