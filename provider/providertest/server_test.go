package providertest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func post(t *testing.T, s *Server, key string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, s.URL()+"/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func content(out map[string]any) string {
	ch := out["choices"].([]any)[0].(map[string]any)
	c, _ := ch["message"].(map[string]any)["content"].(string)
	return c
}

func TestFakeQueueOrder(t *testing.T) {
	s := NewServer(t, "k")
	s.Reply("first")
	s.CallTool("clock.now", `{"tz":"UTC"}`)
	s.Fail(429, 3)
	req := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}

	if code, out := post(t, s, "k", req); code != 200 || content(out) != "first" {
		t.Fatalf("1: %d %v", code, out)
	}
	code, out := post(t, s, "k", req)
	msg := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	call := msg["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if code != 200 || call["id"] != "call_1" || fn["name"] != "clock.now" || fn["arguments"] != `{"tz":"UTC"}` {
		t.Fatalf("2: %d %v", code, out)
	}
	if code, _ := post(t, s, "k", req); code != 429 {
		t.Fatalf("3: status %d, want 429", code)
	}
	if code, out := post(t, s, "k", req); code != 200 || content(out) != "ok" {
		t.Fatalf("empty queue: %d %v", code, out)
	}
	reqs := s.Requests()
	if len(reqs) != 4 || reqs[0].Model != "m" || reqs[0].Messages[0]["content"] != "hi" {
		t.Fatalf("requests %+v", reqs)
	}
}

func TestFakeWrongKey(t *testing.T) {
	s := NewServer(t, "k")
	if code, _ := post(t, s, "nope", map[string]any{"model": "m"}); code != 401 {
		t.Fatalf("wrong key: %d", code)
	}
}

func TestFakeHangReleasedOnCancel(t *testing.T) {
	s := NewServer(t, "k")
	s.Hang()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"model": "m"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer k")
	start := time.Now()
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("hung call returned")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("hang not released by the cancelled request")
	}
}

func TestFakeRespondAfterQueue(t *testing.T) {
	s := NewServer(t, "k")
	s.Reply("queued")
	s.Respond(func(r Request) Answer {
		if r.Messages[len(r.Messages)-1]["role"] == "tool" {
			return Answer{Text: "done"}
		}
		return Answer{Tool: "cells.get", Args: `{"id":"x"}`}
	})
	user := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if _, out := post(t, s, "k", user); content(out) != "queued" {
		t.Fatalf("the queue goes first: %v", out)
	}
	_, out := post(t, s, "k", user)
	fn := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "cells.get" || fn["arguments"] != `{"id":"x"}` {
		t.Fatalf("tool call %v", fn)
	}
	result := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "tool", "content": "{}"}}}
	if _, out := post(t, s, "k", result); content(out) != "done" {
		t.Fatalf("after the tool result: %v", out)
	}
}
