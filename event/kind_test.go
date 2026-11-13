package event_test

import (
	"encoding/json"
	"testing"

	"github.com/igorrius/ganglion/event"
)

// Kinds are stored in the inbox: their values must never change.
func TestKindValues(t *testing.T) {
	for kind, want := range map[event.Kind]string{
		event.KindMessage:       "message",
		event.KindDecision:      "decision",
		event.KindToolResult:    "tool.result",
		event.KindSchedule:      "schedule",
		event.KindCommandDone:   "command.done",
		event.KindModule:        "module",
		event.KindModuleTrigger: "module.trigger",
		event.KindModuleNotify:  "module.notify",
		event.KindModuleBuilt:   "module.built",
		event.KindMemoryReview:  "memory.review",
		event.KindGroupJoined:   "group.joined",
		event.KindGroupRequest:  "group.request",
	} {
		if string(kind) != want {
			t.Errorf("kind %q, want %q", kind, want)
		}
	}
}

// An outbox row stored before buttons existed still decodes, and a message
// without buttons or an edit is stored as before.
func TestOutgoingJSONCompatibleWithStoredRows(t *testing.T) {
	old := `{"ID":"1","IdemKey":"k","Transport":"telegram","Conv":"telegram:1","Text":"hi","Silent":false,"ReplyTo":""}`
	var m event.Outgoing
	if err := json.Unmarshal([]byte(old), &m); err != nil || m.Text != "hi" || m.Buttons != nil || m.Edit != "" {
		t.Fatalf("old row: %+v, %v", m, err)
	}
	b, err := json.Marshal(m)
	if err != nil || string(b) != old {
		t.Fatalf("plain message stored as %s, want %s", b, old)
	}
	m.Buttons, m.Edit = []event.Button{{ID: "B1", Label: "Run"}}, "17"
	b, _ = json.Marshal(m)
	var back event.Outgoing
	if err := json.Unmarshal(b, &back); err != nil || len(back.Buttons) != 1 || back.Buttons[0] != m.Buttons[0] || back.Edit != "17" {
		t.Fatalf("round trip %+v, %v", back, err)
	}
}
