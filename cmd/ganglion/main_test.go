package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &out); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "ganglion "+version {
		t.Fatalf("run --version printed %q, want %q", got, "ganglion "+version)
	}
}

func TestRunRejectsUnknownRole(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"--roles", "core,brain"}, &out)
	if err == nil || !strings.Contains(err.Error(), "brain") {
		t.Fatalf("run --roles core,brain = %v, want error naming the bad role", err)
	}
}

func TestRunStartsAllRolesByDefault(t *testing.T) {
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	t.Setenv("GANGLION_ADMIN", "")
	t.Setenv("GANGLION_METRICS_ADDR", "127.0.0.1:0")
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the node stops as soon as it has started

	if err := run(ctx, nil, &out); err != nil {
		t.Fatalf("run with defaults: %v", err)
	}
	if !strings.Contains(out.String(), "roles=core,edge,worker") {
		t.Fatalf("default run did not start all roles:\n%s", out.String())
	}
}

func TestLogLevel(t *testing.T) {
	for v, want := range map[string]slog.Level{"": slog.LevelInfo, "debug": slog.LevelDebug, "WARN": slog.LevelWarn, "error": slog.LevelError} {
		got, err := logLevel(v)
		if err != nil || got != want {
			t.Errorf("%q: %v, %v", v, got, err)
		}
	}
	if _, err := logLevel("chatty"); err == nil {
		t.Error("a bad level was accepted")
	}
}

func TestRunServesMetrics(t *testing.T) {
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("GANGLION_METRICS_ADDR", "127.0.0.1:0")
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"--roles", "core"}, &out) }()
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
	t.Setenv("GANGLION_DB", filepath.Join(t.TempDir(), "g.db"))
	t.Setenv("GANGLION_METRICS_ADDR", ln.Addr().String())
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"--roles", "core"}, &out); err != nil {
		t.Fatalf("a busy metrics port stopped the node: %v", err)
	}
	if !strings.Contains(out.String(), "metrics are off") {
		t.Fatalf("no warning:\n%s", out.String())
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
