package node_test

import (
	"context"
	"errors"
	"log/slog"
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
