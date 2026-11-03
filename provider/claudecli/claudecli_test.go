package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
)

// The test binary doubles as a fake claude: FAKE_CLAUDE selects what it
// does, FAKE_CLAUDE_RECORD names a file it appends to.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	os.Exit(m.Run())
}

func record(line string) {
	f, err := os.OpenFile(os.Getenv("FAKE_CLAUDE_RECORD"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(line + "\n")
}

func fakeClaude(mode string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "ok":
		b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(stdin)})
		record(string(b))
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"Привет!","usage":{"input_tokens":3,"cache_creation_input_tokens":20,"cache_read_input_tokens":100,"output_tokens":7}}`)
		return 0
	case "limit":
		fmt.Print(`{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached. Your limit will reset at 5pm."}`)
		return 1
	case "login":
		fmt.Print(`{"type":"result","subtype":"success","is_error":true,"result":"Invalid API key · Please run /login"}`)
		return 1
	case "garbage":
		fmt.Fprint(os.Stderr, "boom: something broke")
		return 2
	case "sleep":
		time.Sleep(10 * time.Second)
		return 0
	case "spawn": // like claude with its relay: a child that outlives nothing on its own
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "FAKE_CLAUDE=child")
		if err := child.Start(); err != nil {
			return 5
		}
		record("child " + strconv.Itoa(child.Process.Pid))
		time.Sleep(30 * time.Second)
		return 0
	case "child":
		time.Sleep(30 * time.Second)
		return 0
	case "mcp", "mcp-crash", "mcp-hang":
		return fakeMCP(mode)
	case "slow":
		record("start " + strconv.FormatInt(time.Now().UnixNano(), 10))
		time.Sleep(300 * time.Millisecond)
		record("end " + strconv.FormatInt(time.Now().UnixNano(), 10))
		fmt.Print(`{"type":"result","is_error":false,"result":"ok","usage":{"input_tokens":1,"output_tokens":1}}`)
		return 0
	}
	return 3
}

func fake(t *testing.T, mode string, opts ...Option) (*Client, string) {
	t.Helper()
	rec := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_CLAUDE", mode)
	t.Setenv("FAKE_CLAUDE_RECORD", rec)
	return New("claude", append([]Option{WithCommand(os.Args[0])}, opts...)...), rec
}

var history = []provider.Message{
	{Role: "system", Content: "You are the family assistant."},
	{Role: "system", Content: "Summary of the conversation so far:\nThey talked about the boiler."},
	{Role: "user", Content: "Igor: hi"},
	{Role: "assistant", Content: "Hello!"},
	{Role: "user", Content: "Igor: секретный вопрос про дом"},
}

func TestArgsAndStdin(t *testing.T) {
	c, rec := fake(t, "ok")
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-p", "--model", "haiku", "--output-format", "json", "--no-session-persistence",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "",
		"--system-prompt", "You are the family assistant."}
	if !slices.Equal(got.Args, wantArgs) {
		t.Fatalf("args\n got %q\nwant %q", got.Args, wantArgs)
	}
	for _, a := range got.Args {
		if strings.Contains(a, "секретный") || strings.Contains(a, "boiler") {
			t.Fatal("family text (a message or the summary) is in argv, visible in ps")
		}
	}
	wantStdin := "Summary of the conversation so far:\n  They talked about the boiler.\n\n" +
		"Conversation so far, oldest first:\n\nUser: Igor: hi\n\nAssistant: Hello!\n\nUser: Igor: секретный вопрос про дом"
	if got.Stdin != wantStdin {
		t.Fatalf("stdin\n got %q\nwant %q", got.Stdin, wantStdin)
	}
}

func TestParsesResultAndUsage(t *testing.T) {
	c, _ := fake(t, "ok")
	if c.Name() != "claude" {
		t.Fatalf("Name = %q", c.Name())
	}
	resp, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if m := resp.Message; m.Role != "assistant" || m.Content != "Привет!" || len(m.ToolCalls) != 0 {
		t.Fatalf("message %+v", resp.Message)
	}
	if resp.Usage != (provider.Usage{InputTokens: 123, CacheReadTokens: 100, CacheWriteTokens: 20, OutputTokens: 7}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func kind(t *testing.T, err error) provider.ErrorKind {
	t.Helper()
	e, ok := errors.AsType[*provider.Error](err)
	if !ok {
		t.Fatalf("error %v (%T) is not a provider.Error", err, err)
	}
	if e.Provider != "claude" {
		t.Fatalf("provider %q", e.Provider)
	}
	return e.Kind
}

func TestUsageLimitIsRateLimit(t *testing.T) {
	c, _ := fake(t, "limit")
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history})
	if k := kind(t, err); k != provider.KindRateLimit {
		t.Fatalf("kind %s, want rate_limit", k)
	}
	// A subscription limit lasts hours: the router must not retry it every minute.
	if e, _ := errors.AsType[*provider.Error](err); e.RetryAfter != UsageLimitCooldown {
		t.Fatalf("RetryAfter %v, want %v", e.RetryAfter, UsageLimitCooldown)
	}
}

func TestNotLoggedInIsAuth(t *testing.T) {
	c, _ := fake(t, "login")
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history})
	if k := kind(t, err); k != provider.KindAuth {
		t.Fatalf("kind %s, want auth", k)
	}
}

func TestGarbageIsUnavailable(t *testing.T) {
	c, _ := fake(t, "garbage")
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history})
	if k := kind(t, err); k != provider.KindUnavailable || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("garbage: %v", err)
	}
	if strings.Contains(err.Error(), "секретный") {
		t.Fatal("error carries the prompt")
	}
}

func TestMissingBinaryIsUnavailable(t *testing.T) {
	c := New("claude", WithCommand(filepath.Join(t.TempDir(), "no-such-claude")))
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history})
	if k := kind(t, err); k != provider.KindUnavailable {
		t.Fatalf("kind %s, want unavailable", k)
	}
}

func TestTimeoutKillsProcess(t *testing.T) {
	c, _ := fake(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Chat(ctx, provider.ChatRequest{Model: "haiku", Messages: history})
	if k := kind(t, err); k != provider.KindTimeout {
		t.Fatalf("kind %s, want timeout", k)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("returned after %v", d)
	}
}

func TestCancelIsReturnedAsCanceled(t *testing.T) {
	c, _ := fake(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := c.Chat(ctx, provider.ChatRequest{Model: "haiku", Messages: history})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	c, rec := fake(t, "slow", WithMaxConcurrent(2))
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	raw, _ := os.ReadFile(rec)
	type ev struct {
		at    int64
		delta int
	}
	var evs []ev
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		kind, ns, _ := strings.Cut(line, " ")
		v, _ := strconv.ParseInt(ns, 10, 64)
		d := 1
		if kind == "end" {
			d = -1
		}
		evs = append(evs, ev{v, d})
	}
	slices.SortFunc(evs, func(a, b ev) int {
		if a.at != b.at {
			return int(a.at - b.at)
		}
		return a.delta - b.delta
	})
	alive, peak := 0, 0
	for _, e := range evs {
		alive += e.delta
		peak = max(peak, alive)
	}
	if len(evs) != 6 || peak != 2 {
		t.Fatalf("%d events, peak %d alive; want 6 events, peak 2", len(evs), peak)
	}
}

func TestToolsWithoutExecRejected(t *testing.T) {
	c, _ := fake(t, "ok")
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history,
		Tools: []provider.Tool{{Name: "x", InputSchema: json.RawMessage(`{}`)}}})
	if k := kind(t, err); k != provider.KindBadRequest {
		t.Fatalf("kind %s, want bad_request", k)
	}
}

func TestLiveClaude(t *testing.T) {
	if os.Getenv("GANGLION_LIVE_CLAUDE") == "" {
		t.Skip("set GANGLION_LIVE_CLAUDE=1 to call the real claude")
	}
	resp, err := New("claude").Chat(context.Background(), provider.ChatRequest{Model: "haiku",
		Messages: []provider.Message{{Role: "system", Content: "Answer with one word."}, {Role: "user", Content: "Say OK"}}})
	if err != nil || strings.TrimSpace(resp.Message.Content) == "" {
		t.Fatalf("live: %+v, %v", resp, err)
	}
	t.Logf("live answer %q, usage %+v", resp.Message.Content, resp.Usage)
}
