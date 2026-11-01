package node

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Run(ctx, []Role{Core, Edge}, logger) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	if !strings.Contains(logs.String(), "roles=core,edge") {
		t.Errorf("startup log does not name the roles:\n%s", logs.String())
	}
}

func TestRunRequiresRoles(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if err := Run(context.Background(), nil, logger); err == nil {
		t.Fatal("Run with no roles returned nil, want error")
	}
}
