package cmd

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunStartsAllRolesByDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("GANGLION_METRICS_ADDR", "127.0.0.1:0")
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the node stops as soon as it has started
	if err := Execute(ctx, []string{"ganglion", "run"}, &out); err != nil {
		t.Fatalf("run with defaults: %v", err)
	}
	if !strings.Contains(out.String(), "roles=core,edge,worker") {
		t.Fatalf("default run did not start all roles:\n%s", out.String())
	}
}

func TestRunServesMetrics(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("GANGLION_METRICS_ADDR", "127.0.0.1:0")
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Execute(ctx, []string{"ganglion", "run", "--roles", "core"}, &out) }()
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
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("GANGLION_METRICS_ADDR", ln.Addr().String())
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Execute(ctx, []string{"ganglion", "run", "--roles", "core"}, &out); err != nil {
		t.Fatalf("a busy metrics port stopped the node: %v", err)
	}
	if !strings.Contains(out.String(), "metrics are off") {
		t.Fatalf("no warning:\n%s", out.String())
	}
}

// The built binary knows what it is, with and without the Makefile's -X.
func TestBinaryVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
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
