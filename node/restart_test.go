package node_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/node"
)

func TestRunReturnsErrRestart(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(node.ErrRestart)
	err := node.Run(ctx, []node.Role{node.Core}, func() ([]node.Component, error) { return nil, nil }, slog.New(slog.DiscardHandler))
	if !errors.Is(err, node.ErrRestart) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWithoutCoreReturnsErrRestart(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(node.ErrRestart)
	err := node.Run(ctx, []node.Role{node.Edge}, nil, slog.New(slog.DiscardHandler))
	if !errors.Is(err, node.ErrRestart) {
		t.Fatalf("err = %v", err)
	}
}

// The log says why the node stopped: a restart, not a crash.
func TestRunLogsARestart(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(node.ErrRestart)
	_ = node.Run(ctx, []node.Role{node.Edge}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "restart requested") {
		t.Fatalf("logs:\n%s", logs.String())
	}
	logs.Reset()
	ctx, stop := context.WithCancel(context.Background())
	stop()
	_ = node.Run(ctx, []node.Role{node.Edge}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if strings.Contains(logs.String(), "restart requested") {
		t.Fatalf("a plain stop logged a restart:\n%s", logs.String())
	}
}
