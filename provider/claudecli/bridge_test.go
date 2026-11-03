package claudecli

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/mcp"
	"github.com/igorrius/ganglion/provider"
)

// mcpConfig is the file the client writes for the CLI.
type mcpConfig struct {
	MCPServers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	} `json:"mcpServers"`
}

// seen is what the fake claude records in a tools mode.
type seen struct {
	Args    []string          `json:"args"`
	Config  string            `json:"config"`
	Raw     string            `json:"raw"`
	Modes   map[string]string `json:"modes"`
	Replies []string          `json:"replies"`
	Closed  bool              `json:"closed"` // the socket closed early
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func permOf(path string) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%v", fi.Mode().Perm())
}

// fakeMCP is the fake claude with tools: it reads the config the client
// wrote, connects to the socket as the relay would, calls one tool and
// records what it saw.
func fakeMCP(mode string) int {
	s := seen{Args: os.Args[1:], Config: argAfter(os.Args[1:], "--mcp-config")}
	raw, _ := os.ReadFile(s.Config)
	s.Raw = string(raw)
	var cfg mcpConfig
	_ = json.Unmarshal(raw, &cfg)
	srv := cfg.MCPServers["ganglion"]
	socket := argAfter(srv.Args, "--socket")
	s.Modes = map[string]string{"dir": permOf(filepath.Dir(s.Config)), "config": permOf(s.Config), "socket": permOf(socket)}
	token := srv.Env[mcp.TokenEnv]
	defer func() {
		b, _ := json.Marshal(s)
		record(string(b))
	}()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return 4
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	_, _ = fmt.Fprintf(conn, "%s\n", token)
	tool := os.Getenv("FAKE_CLAUDE_TOOL")
	for i, req := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"x":1}}}`,
	} {
		if _, err := io.WriteString(conn, req+"\n"); err != nil {
			s.Closed = true
			break
		}
		if i == 1 {
			continue
		}
		line, err := r.ReadString('\n')
		if err != nil {
			s.Closed = true
			break
		}
		s.Replies = append(s.Replies, strings.TrimSpace(line))
	}
	switch mode {
	case "mcp-crash":
		return 1
	case "mcp-hang":
		time.Sleep(30 * time.Second)
		return 0
	}
	fmt.Print(`{"type":"result","is_error":false,"result":"done with tools","usage":{"input_tokens":1,"output_tokens":1}}`)
	return 0
}

func readSeen(t *testing.T, rec string) seen {
	t.Helper()
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	var s seen
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &s); err != nil {
		t.Fatalf("record %q: %v", raw, err)
	}
	return s
}

// execRecorder is the agent's side: it records the calls and answers.
type execRecorder struct {
	mu    sync.Mutex
	calls []provider.ToolCall
}

func (e *execRecorder) exec(_ context.Context, c provider.ToolCall) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, c)
	return "2 jobs", nil
}

var tools = []provider.Tool{{Name: "schedule.list", Description: "List jobs.", InputSchema: json.RawMessage(`{"type":"object"}`)}}

// toolsFake is a fake claude in mode that runs in work dir dir with the
// given relay command.
func toolsFake(t *testing.T, mode, dir string) (*Client, string) {
	t.Helper()
	t.Setenv("FAKE_CLAUDE_TOOL", "schedule_list")
	return fake(t, mode, WithWorkDir(dir), WithRelay("/opt/ganglion/bin/ganglion"))
}

func TestToolsOverTheBridge(t *testing.T) {
	work := t.TempDir()
	c, rec := toolsFake(t, "mcp", work)
	x := &execRecorder{}
	resp, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec})
	if err != nil || resp.Message.Content != "done with tools" {
		t.Fatalf("Chat: %+v, %v", resp, err)
	}
	s := readSeen(t, rec)
	for _, want := range [][]string{
		{"--tools", ""}, {"--strict-mcp-config"}, {"--mcp-config", s.Config}, {"--allowedTools", "mcp__ganglion__*"},
	} {
		if i := slices.Index(s.Args, want[0]); i < 0 || !slices.Equal(s.Args[i:i+len(want)], want) {
			t.Errorf("argv %q lacks %q", s.Args, want)
		}
	}
	if filepath.Dir(filepath.Dir(s.Config)) != work || filepath.Base(s.Config) != "mcp.json" {
		t.Errorf("config %s is not <work dir>/<run dir>/mcp.json", s.Config)
	}
	if s.Modes["dir"] != "-rwx------" || s.Modes["config"] != "-rw-------" || s.Modes["socket"] != "-rw-------" {
		t.Errorf("modes %v", s.Modes)
	}
	var cfg mcpConfig
	if err := json.Unmarshal([]byte(s.Raw), &cfg); err != nil || len(cfg.MCPServers) != 1 {
		t.Fatalf("config %s: %v", s.Raw, err)
	}
	srv := cfg.MCPServers["ganglion"]
	sock := filepath.Join(filepath.Dir(s.Config), "mcp.sock")
	if srv.Command != "/opt/ganglion/bin/ganglion" || !slices.Equal(srv.Args, []string{"mcp-relay", "--socket", sock}) || len(srv.Env) != 1 {
		t.Errorf("server %+v", srv)
	}
	if b, err := hex.DecodeString(srv.Env[mcp.TokenEnv]); err != nil || len(b) != 32 {
		t.Errorf("token %q is not 32 bytes in hex", srv.Env[mcp.TokenEnv])
	}
	if len(s.Replies) != 3 || !strings.Contains(s.Replies[1], `"schedule_list"`) || !strings.Contains(s.Replies[2], `"text":"2 jobs"`) {
		t.Errorf("replies %q", s.Replies)
	}
	if len(x.calls) != 1 || x.calls[0].Name != "schedule.list" || string(x.calls[0].Args) != `{"x":1}` {
		t.Errorf("exec got %+v", x.calls)
	}
	if _, err := os.Stat(filepath.Dir(s.Config)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("run dir left behind: %v", err)
	}
}

func TestTokenNotInArgv(t *testing.T) {
	c, rec := toolsFake(t, "mcp", t.TempDir())
	x := &execRecorder{}
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec}); err != nil {
		t.Fatal(err)
	}
	s := readSeen(t, rec)
	var cfg mcpConfig
	_ = json.Unmarshal([]byte(s.Raw), &cfg)
	token := cfg.MCPServers["ganglion"].Env[mcp.TokenEnv]
	if token == "" {
		t.Fatal("no token in the config")
	}
	for _, a := range slices.Concat(s.Args, cfg.MCPServers["ganglion"].Args) {
		if strings.Contains(a, token) {
			t.Fatalf("the token is in argv: %q", a)
		}
	}
	if os.Getenv(mcp.TokenEnv) != "" {
		t.Fatal("the token leaked into this process's environment")
	}
}

func TestRunDirRemovedOnKill(t *testing.T) {
	work := t.TempDir()
	c, rec := toolsFake(t, "mcp-crash", work)
	x := &execRecorder{}
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec})
	if err == nil {
		t.Fatal("a crashed claude answered")
	}
	s := readSeen(t, rec)
	if len(x.calls) != 1 {
		t.Fatalf("exec got %+v", x.calls)
	}
	if _, err := os.Stat(filepath.Dir(s.Config)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("run dir left behind after a crash: %v", err)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("work dir holds %v", left)
	}
}

func TestRunDirRemovedOnTimeout(t *testing.T) {
	work := t.TempDir()
	c, _ := toolsFake(t, "mcp-hang", work)
	x := &execRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Chat(ctx, provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec})
	if k := kind(t, err); k != provider.KindTimeout {
		t.Fatalf("kind %s, want timeout", k)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("work dir holds %v after a kill", left)
	}
}

// ADR-0036, decision 1: a socket path past the limit moves the run dir to a short
// private temporary directory.
func TestRunDirFallsBackForLongPaths(t *testing.T) {
	work := filepath.Join(t.TempDir(), strings.Repeat("w", 60), strings.Repeat("x", 60))
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	c, rec := toolsFake(t, "mcp", work)
	x := &execRecorder{}
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec}); err != nil {
		t.Fatal(err)
	}
	s := readSeen(t, rec)
	run := filepath.Dir(s.Config)
	if strings.HasPrefix(run, work) || filepath.Dir(run) != filepath.Clean(os.TempDir()) {
		t.Fatalf("run dir %s, want one directly under %s", run, os.TempDir())
	}
	if s.Modes["dir"] != "-rwx------" || len(x.calls) != 1 {
		t.Fatalf("modes %v, calls %d", s.Modes, len(x.calls))
	}
	if _, err := os.Stat(run); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("fallback run dir left behind: %v", err)
	}
}

func TestSocketPathTooLongEvenInTemp(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), strings.Repeat("t", 120)))
	_ = os.MkdirAll(os.Getenv("TMPDIR"), 0o700)
	work := filepath.Join(t.TempDir(), strings.Repeat("w", 120))
	_ = os.MkdirAll(work, 0o700)
	c, _ := toolsFake(t, "mcp", work)
	x := &execRecorder{}
	_, err := c.Chat(context.Background(), provider.ChatRequest{Model: "haiku", Messages: history, Tools: tools, Exec: x.exec})
	if k := kind(t, err); k != provider.KindUnavailable || !strings.Contains(err.Error(), "socket path") {
		t.Fatalf("Chat: %v", err)
	}
}

// openTest opens a bridge serving tools in a short directory.
func openTest(t *testing.T, exec provider.Exec, auth time.Duration) *bridge {
	t.Helper()
	b, err := openBridge(context.Background(), t.TempDir(), "/bin/ganglion", auth, mcp.NewServer("ganglion", "t", tools, exec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	return b
}

// readsEOF reports whether conn is closed by the other side within 5 s
// without sending anything: EOF, or a reset when the bridge closed it
// with data unread.
func readsEOF(t *testing.T, conn net.Conn) bool {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(make([]byte, 1))
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return false
	}
	return n == 0 && err != nil
}

func dial(t *testing.T, b *bridge) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", b.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRelayRejectsWrongToken(t *testing.T) {
	x := &execRecorder{}
	b := openTest(t, x.exec, 5*time.Second)

	wrong := dial(t, b)
	_, _ = io.WriteString(wrong, strings.Repeat("0", len(b.token))+"\n"+`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if !readsEOF(t, wrong) {
		t.Fatal("a connection with a wrong token was not closed")
	}
	// The relay with a wrong token gets nothing back.
	var out strings.Builder
	err := mcp.Relay(context.Background(), b.socket, "nope", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if out.Len() != 0 {
		t.Fatalf("a wrong token got %q (%v)", out.String(), err)
	}

	// The right token is served...
	out.Reset()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_list","arguments":{}}}` + "\n"
	if err := mcp.Relay(context.Background(), b.socket, b.token, strings.NewReader(in), &out); err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if !strings.Contains(out.String(), `"text":"2 jobs"`) || len(x.calls) != 1 {
		t.Fatalf("relay got %q, exec %d calls", out.String(), len(x.calls))
	}
	// ...once: a second connection with the right token is closed too.
	again := dial(t, b)
	_, _ = io.WriteString(again, b.token+"\n"+`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if !readsEOF(t, again) {
		t.Fatal("a second authenticated connection was served")
	}
}

func TestSilentConnectionIsClosed(t *testing.T) {
	b := openTest(t, (&execRecorder{}).exec, 100*time.Millisecond)
	silent := dial(t, b)
	if !readsEOF(t, silent) {
		t.Fatal("a connection that sent no token stayed open")
	}
	// The run still gets its one connection after that.
	var out strings.Builder
	if err := mcp.Relay(context.Background(), b.socket, b.token, strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"ping"}`+"\n"), &out); err != nil || !strings.Contains(out.String(), `"id":9`) {
		t.Fatalf("relay after a silent connection: %q, %v", out.String(), err)
	}
}

func TestOversizeTokenLineIsClosed(t *testing.T) {
	b := openTest(t, (&execRecorder{}).exec, 5*time.Second)
	big := dial(t, b)
	go func() { _, _ = big.Write([]byte(strings.Repeat("a", 1<<20))) }()
	if !readsEOF(t, big) {
		t.Fatal("a huge first line was read on")
	}
}

// ADR-0036, decision 6: close stops serving, waits for the serve goroutine and the
// calls it runs, and removes the run dir.
func TestBridgeCloseWaitsForServe(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	b, err := openBridge(context.Background(), t.TempDir(), "/bin/ganglion", time.Second,
		mcp.NewServer("ganglion", "t", tools, func(ctx context.Context, _ provider.ToolCall) (string, error) {
			close(started)
			<-ctx.Done()
			close(ended)
			return "", ctx.Err()
		}))
	if err != nil {
		t.Fatal(err)
	}
	conn := dial(t, b)
	_, _ = io.WriteString(conn, b.token+"\n"+`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_list"}}`+"\n")
	<-started
	b.close()
	select {
	case <-b.done:
	default:
		t.Fatal("close returned before the serve goroutine ended")
	}
	select {
	case <-ended:
	default:
		t.Fatal("close returned before the running call ended")
	}
	if _, err := os.Stat(b.dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("run dir left: %v", err)
	}
	b.close() // twice is harmless
}

// Silent connections of the same user cannot hold the real relay up:
// each is authenticated on its own, and the first valid one wins.
func TestSilentConnectionsDoNotDelayTheRelay(t *testing.T) {
	b := openTest(t, (&execRecorder{}).exec, authTimeout)
	var silent []net.Conn
	for range 5 {
		silent = append(silent, dial(t, b))
	}
	start := time.Now()
	var out strings.Builder
	err := mcp.Relay(context.Background(), b.socket, b.token, strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"ping"}`+"\n"), &out)
	if err != nil || !strings.Contains(out.String(), `"id":5`) {
		t.Fatalf("relay: %q, %v", out.String(), err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the relay waited %v behind silent connections", d)
	}
	for _, c := range silent {
		if !readsEOF(t, c) {
			t.Fatal("a silent connection stayed open")
		}
	}
}
