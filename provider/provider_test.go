package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	plain, err := json.Marshal(Message{Role: "user", Content: "hi"})
	if err != nil || string(plain) != `{"role":"user","content":"hi"}` {
		t.Fatalf("plain message = %s, %v", plain, err)
	}
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "clock.now", Args: json.RawMessage(`{"x":1}`)}}},
		{Role: "tool", ToolCallID: "call_1", Content: "12:00"},
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	var back []Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].ToolCalls[0].Name != "clock.now" || string(back[0].ToolCalls[0].Args) != `{"x":1}` ||
		back[1].ToolCallID != "call_1" || back[1].Content != "12:00" {
		t.Fatalf("round trip = %+v from %s", back, b)
	}
}

func TestToolJSONIsMCPShape(t *testing.T) {
	b, _ := json.Marshal(Tool{Name: "memory.read", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)})
	if string(b) != `{"name":"memory.read","description":"d","inputSchema":{"type":"object"}}` {
		t.Fatalf("tool = %s", b)
	}
}

func TestFallback(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&Error{Kind: KindRateLimit}, true},
		{&Error{Kind: KindTimeout}, true},
		{&Error{Kind: KindUnavailable}, true},
		{&Error{Kind: KindAuth}, false},
		{&Error{Kind: KindBadRequest}, false},
		{fmt.Errorf("wrapped: %w", &Error{Kind: KindRateLimit}), true},
		{context.Canceled, false},
		{fmt.Errorf("call: %w", context.DeadlineExceeded), true},
		{errors.New("plain"), false},
		{nil, false},
	} {
		if got := Fallback(tc.err); got != tc.want {
			t.Errorf("Fallback(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestErrorText(t *testing.T) {
	e := &Error{Provider: "or", Kind: KindRateLimit, Status: 429, Msg: "slow down"}
	if e.Error() != "provider or: rate_limit: slow down" {
		t.Fatalf("Error() = %q", e.Error())
	}
}
