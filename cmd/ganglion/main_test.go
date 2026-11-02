package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
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
