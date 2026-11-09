package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router"
)

const key = "sk-or-v1-secret-key-0123456789"

func user(text string) []provider.Message { return []provider.Message{{Role: "user", Content: text}} }

func TestChatText(t *testing.T) {
	fake := providertest.NewServer(t, key)
	fake.Reply("hello")
	c := New("or", fake.URL(), key)
	if c.Name() != "or" {
		t.Fatalf("Name = %q", c.Name())
	}
	resp, err := c.Chat(context.Background(), provider.ChatRequest{Model: "m1", Messages: user("hi"), MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Role != "assistant" || resp.Message.Content != "hello" || len(resp.Message.ToolCalls) != 0 {
		t.Fatalf("message %+v", resp.Message)
	}
	if resp.Usage != (provider.Usage{InputTokens: 10, CacheReadTokens: 4, OutputTokens: 5}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
	r := fake.Requests()[0]
	if r.Model != "m1" || r.MaxTokens != 100 {
		t.Fatalf("request %+v", r)
	}
}

func TestToolsTranslated(t *testing.T) {
	fake := providertest.NewServer(t, key)
	tool := provider.Tool{Name: "clock_now", Description: "current time", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
	if _, err := New("or", fake.URL(), key).Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: user("hi"), Tools: []provider.Tool{tool}}); err != nil {
		t.Fatal(err)
	}
	tools := fake.Requests()[0].Tools
	if len(tools) != 1 || tools[0]["type"] != "function" {
		t.Fatalf("tools %v", tools)
	}
	fn := tools[0]["function"].(map[string]any)
	params, _ := json.Marshal(fn["parameters"])
	if fn["name"] != "clock_now" || fn["description"] != "current time" || string(params) != `{"properties":{},"type":"object"}` {
		t.Fatalf("function %v", fn)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	fake := providertest.NewServer(t, key)
	fake.CallTool("clock_now", `{"tz":"UTC"}`)
	c := New("or", fake.URL(), key)
	resp, err := c.Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: user("time?")})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "clock_now" || string(calls[0].Args) != `{"tz":"UTC"}` {
		t.Fatalf("tool calls %+v", calls)
	}
	history := append(user("time?"), resp.Message, provider.Message{Role: "tool", ToolCallID: "call_1", Content: "12:00"})
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: history}); err != nil {
		t.Fatal(err)
	}
	msgs := fake.Requests()[1].Messages
	if len(msgs) != 3 {
		t.Fatalf("messages %v", msgs)
	}
	wireCall := msgs[1]["tool_calls"].([]any)[0].(map[string]any)
	if wireCall["id"] != "call_1" || wireCall["type"] != "function" || wireCall["function"].(map[string]any)["arguments"] != `{"tz":"UTC"}` {
		t.Fatalf("assistant tool call on the wire %v", msgs[1])
	}
	if msgs[2]["role"] != "tool" || msgs[2]["tool_call_id"] != "call_1" || msgs[2]["content"] != "12:00" {
		t.Fatalf("tool result on the wire %v", msgs[2])
	}
}

func TestErrorKinds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status, retry int
		kind          provider.ErrorKind
		after         time.Duration
	}{
		{429, 7, provider.KindRateLimit, 7 * time.Second},
		{500, 0, provider.KindUnavailable, 0},
		{503, 0, provider.KindUnavailable, 0},
		{401, 0, provider.KindAuth, 0},
		{403, 0, provider.KindAuth, 0},
		{400, 0, provider.KindBadRequest, 0},
		{404, 0, provider.KindBadRequest, 0},
	} {
		fake := providertest.NewServer(t, key)
		fake.Fail(tc.status, tc.retry)
		_, err := New("or", fake.URL(), key).Chat(ctx, provider.ChatRequest{Model: "m", Messages: user("x")})
		e, ok := errors.AsType[*provider.Error](err)
		if !ok || e.Kind != tc.kind || e.Status != tc.status || e.RetryAfter != tc.after || e.Provider != "or" {
			t.Errorf("status %d: %v (%+v), want %s", tc.status, err, e, tc.kind)
		}
	}
	fake := providertest.NewServer(t, key)
	fake.Hang()
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := New("or", fake.URL(), key).Chat(short, provider.ChatRequest{Model: "m", Messages: user("x")})
	if e, ok := errors.AsType[*provider.Error](err); !ok || e.Kind != provider.KindTimeout {
		t.Errorf("hang: %v, want timeout", err)
	}
	cancelled, cancel2 := context.WithCancel(ctx)
	cancel2()
	_, err = New("or", fake.URL(), key).Chat(cancelled, provider.ChatRequest{Model: "m", Messages: user("x")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v, want context.Canceled and no fallback", err)
	}
}

func TestKeyNeverInErrors(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"bad header `+r.Header.Get("Authorization")+`"}}`)
	}))
	t.Cleanup(echo.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "oops "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(broken.Close)
	for _, base := range []string{"http://127.0.0.1:1/v1", echo.URL, broken.URL} {
		_, err := New("or", base, key).Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: user("x")})
		if err == nil {
			t.Fatalf("%s: no error", base)
		}
		for e := err; e != nil; e = errors.Unwrap(e) {
			if strings.Contains(e.Error(), "secret-key") {
				t.Fatalf("%s: error leaks the key: %v", base, e)
			}
		}
	}
}

// Some backends send "" for a tool without parameters; it arrives as an
// empty object, which the tool argument check accepts (ruling R21).
func TestEmptyToolArgumentsBecomeAnObject(t *testing.T) {
	for _, args := range []string{"", "  \n"} {
		fake := providertest.NewServer(t, key)
		fake.CallTool("memory_audit", args)
		resp, err := New("or", fake.URL(), key).Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: user("audit")})
		if err != nil {
			t.Fatal(err)
		}
		if calls := resp.Message.ToolCalls; len(calls) != 1 || string(calls[0].Args) != `{}` {
			t.Fatalf("arguments %q: tool calls %+v", args, calls)
		}
	}
}

// effortBody sends one request with effort to a server that records the
// body it got.
func effortBody(t *testing.T, effort router.Effort) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := New("t", srv.URL, "k")
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "m", Effort: effort,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestEffortIsSentAsReasoningEffort(t *testing.T) {
	if body := effortBody(t, router.EffortLow); body["reasoning_effort"] != "low" {
		t.Fatalf("body = %v", body)
	}
}

func TestNoEffortSendsNoField(t *testing.T) {
	if body := effortBody(t, ""); body == nil {
		t.Fatal("no request")
	} else if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("an unset effort is sent: %v", body)
	}
}
