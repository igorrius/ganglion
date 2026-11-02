package node

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fn struct {
	name string
	run  func(context.Context) error
}

func (f fn) Name() string                  { return f.name }
func (f fn) Run(ctx context.Context) error { return f.run(ctx) }
func waitCtx(ctx context.Context) error    { <-ctx.Done(); return nil }
func failNow(context.Context) error        { return errors.New("broken") }
func noCore() ([]Component, error)         { panic("core built without the core role") }
func coreOf(c ...Component) func() ([]Component, error) {
	return func() ([]Component, error) { return c, nil }
}

func TestRunAllStopsTheRestWhenOneFails(t *testing.T) {
	err := RunAll(context.Background(), []Component{fn{"waits", waitCtx}, fn{"breaks", failNow}})
	if err == nil || err.Error() != "breaks: broken" {
		t.Fatalf("RunAll = %v, want the failing component's error with its name", err)
	}
}

func TestRunAllReturnsNilWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := RunAll(ctx, []Component{fn{"a", waitCtx}, fn{"b", waitCtx}}); err != nil {
		t.Fatalf("RunAll = %v", err)
	}
}

func TestRunWithoutCoreWaitsAndLogsTheRoles(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Run(ctx, []Role{Edge, Worker}, noCore, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "roles=edge,worker") || !strings.Contains(logs.String(), "node stopped") {
		t.Fatalf("logs:\n%s", logs.String())
	}
}

func TestRunRequiresRoles(t *testing.T) {
	if err := Run(context.Background(), nil, noCore, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Run with no roles returned nil, want error")
	}
}

func TestRunCoreRunsItsComponents(t *testing.T) {
	err := Run(context.Background(), []Role{Core}, coreOf(fn{"breaks", failNow}), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Run = %v, want the component's error", err)
	}
}

// A stop asked for while the core is being built is not an error: a
// migration cut short by the signal does not fail the process.
func TestRunIgnoresBuildErrorsAfterAStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	build := func() ([]Component, error) { return nil, context.Canceled }
	if err := Run(ctx, []Role{Core}, build, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("Run = %v, want nil after a stop", err)
	}
}
