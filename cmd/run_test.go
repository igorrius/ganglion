package cmd

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/internal/memlimit"
	"github.com/igorrius/ganglion/internal/testinputs"
)

func TestRunStartsAllRolesByDefault(t *testing.T) {
	clearEnv(t)
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the node stops as soon as it has started
	args := append([]string{"ganglion", "run"}, nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")...)
	if err := Execute(ctx, args, &out, &out); err != nil {
		t.Fatalf("run with defaults: %v", err)
	}
	if !strings.Contains(out.String(), "roles=core,edge,worker") {
		t.Fatalf("default run did not start all roles:\n%s", out.String())
	}
}

func TestRunServesMetrics(t *testing.T) {
	clearEnv(t)
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	args := append([]string{"ganglion", "run", "--roles", "core"}, nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")...)
	go func() { done <- Execute(ctx, args, &out, &out) }()
	var addr string
	for i := 0; i < 200 && addr == ""; i++ {
		time.Sleep(10 * time.Millisecond)
		if _, rest, ok := strings.Cut(out.String(), "metrics=http://"); ok {
			addr, _, _ = strings.Cut(rest, "/metrics")
		}
	}
	if addr == "" {
		cancel()
		t.Fatalf("no metrics address logged:\n%s", out.String())
	}
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if err := <-done; err != nil || resp.StatusCode != 200 {
		t.Fatalf("status %d, run %v", resp.StatusCode, err)
	}
}

func TestBusyMetricsPortDoesNotStopTheNode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	clearEnv(t)
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := append([]string{"ganglion", "run", "--roles", "core"}, nodeFiles(t, "metrics: {addr: \""+ln.Addr().String()+"\"}\nlog: {dir: off}\n")...)
	if err := Execute(ctx, args, &out, &out); err != nil {
		t.Fatalf("a busy metrics port stopped the node: %v", err)
	}
	if !strings.Contains(out.String(), "metrics are off") {
		t.Fatalf("no warning:\n%s", out.String())
	}
}

func TestBootstrapFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ganglion.yaml")
	mustWrite(t, path, "node: {tz: UTC}\nagent: {limits: {daily_turns: 7}}\ntelegram: {token: env:BOT}\n")
	env := map[string]string{"BOT": "t0k"}
	b, err := bootstrap(flags.Run{Config: path, DB: filepath.Join(dir, "g.db")}, fakeEnv(env), fakeEnv(env).get, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if b.Agent.TZ.String() != "UTC" || b.Agent.DailyTurns != 7 || b.BotToken != "t0k" {
		t.Fatalf("got %+v", b)
	}
}

func TestRetiredVariableFailsTheStart(t *testing.T) {
	env := fakeEnv{"GANGLION_TZ": "UTC"}
	_, err := bootstrap(flags.Run{Config: "none.yaml", DB: "g.db"}, env, env.get, os.ReadFile)
	if err == nil || !strings.Contains(err.Error(), "set node.tz in ganglion.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryLimitInBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ganglion.yaml")
	mustWrite(t, path, "node: {memory_limit: 100MiB}\n")
	b, err := bootstrap(flags.Run{Config: path, DB: filepath.Join(dir, "g.db")}, fakeEnv{}, fakeEnv{}.get, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := (memlimit.Limit{Bytes: 100 << 20, Source: memlimit.SourceConfig}); b.MemoryLimit != want {
		t.Fatalf("MemoryLimit = %+v, want %+v", b.MemoryLimit, want)
	}
}

// fakeEnv is the environment of a bootstrap test: its secrets and its
// variables.
type fakeEnv map[string]string

func (e fakeEnv) Secret(name string) (string, error) { return e[name], nil }

func (e fakeEnv) get(k string) string { return e[k] }

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The built binary knows what it is, with and without the Makefile's -X.
func TestBinaryVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	testinputs.Build(t, "..")
	dir := t.TempDir()
	for _, tc := range []struct {
		ldflags, want string
	}{
		{"-X github.com/igorrius/ganglion/internal/buildinfo.version=v9.9.9-test", "version: v9.9.9-test\n"},
		{"", "commit: "},
	} {
		bin := filepath.Join(dir, "ganglion")
		if out, err := exec.Command("go", "build", "-ldflags", tc.ldflags, "-o", bin, "..").CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		out, err := exec.Command(bin, "version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "version: \n") {
			t.Fatalf("ldflags %q: %v\n%s", tc.ldflags, err, out)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the logger and the test at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
