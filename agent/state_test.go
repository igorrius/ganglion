package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
)

func TestDecodeState(t *testing.T) {
	st, err := DecodeState(nil)
	if err != nil || st.V != 1 || len(st.Messages) != 0 {
		t.Fatalf("nil state = %+v, %v", st, err)
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	in := State{V: 1, Session: "01S", Started: at, Last: at.Add(time.Minute), Summary: "s",
		Messages: []provider.Message{{Role: "user", Content: "Igor: hi"}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["started_ms"] != float64(at.UnixMilli()) || m["session"] != "01S" {
		t.Fatalf("wire form %s", raw)
	}
	back, err := DecodeState(raw)
	if err != nil || !back.Started.Equal(at) || !back.Last.Equal(at.Add(time.Minute)) || back.Summary != "s" || back.Messages[0].Content != "Igor: hi" {
		t.Fatalf("round trip %+v, %v", back, err)
	}
	if _, err := DecodeState(json.RawMessage(`{"v":2}`)); err == nil {
		t.Fatal("v:2 accepted")
	}
}

func TestCompactSplitKeepsToolCallWhole(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "1", Name: "x"}}},
		{Role: "tool", ToolCallID: "1", Content: "r"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}
	if got := compactSplit(msgs, 3); got != 1 {
		t.Fatalf("split %d, want 1: keeping 3 would start at the tool result", got)
	}
	if got := compactSplit(msgs, 2); got != 3 {
		t.Fatalf("split %d, want 3", got)
	}
	if got := compactSplit(msgs, 5); got != 0 {
		t.Fatalf("split %d, want 0: nothing to compact", got)
	}
}
