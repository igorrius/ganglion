package runner_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/runner"
)

// fakeTool answers with its name; a call waits on gate when it is set.
type fakeTool struct {
	name   string
	gate   chan struct{}
	closed atomic.Bool
}

func (f *fakeTool) Call(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.closed.Load() {
		return nil, context.Canceled
	}
	return json.RawMessage(`"` + f.name + `"`), nil
}

func (f *fakeTool) Shutdown(context.Context) error { f.closed.Store(true); return nil }

func callName(t *testing.T, s *runner.SwapTool) string {
	t.Helper()
	out, err := s.Call(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n string
	_ = json.Unmarshal(out, &n)
	return n
}

// R-RUN-8: new calls go to the new version at once; a call in flight
// finishes on the old one, which is shut down only after it.
func TestSwapKeepsCallsInFlightOnTheOldVersion(t *testing.T) {
	v1 := &fakeTool{name: "v1", gate: make(chan struct{})}
	v2 := &fakeTool{name: "v2"}
	s := runner.NewSwapTool(v1)
	var (
		wg  sync.WaitGroup
		got string
	)
	wg.Go(func() { got = callName(t, s) })
	time.Sleep(20 * time.Millisecond) // the call is inside v1
	if err := s.Swap(v2); err != nil {
		t.Fatal(err)
	}
	if n := callName(t, s); n != "v2" {
		t.Fatalf("a new call went to %s", n)
	}
	if v1.closed.Load() {
		t.Fatal("v1 was shut down with a call in flight")
	}
	close(v1.gate)
	wg.Wait()
	if got != "v1" {
		t.Fatalf("the call in flight answered %q", got)
	}
	deadline := time.Now().Add(time.Second)
	for !v1.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("v1 was never shut down")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManifestABI(t *testing.T) {
	m, err := runner.ParseManifest([]byte("name: x\n"))
	if err != nil || m.ABI != 1 {
		t.Fatalf("missing abi: %d, %v", m.ABI, err)
	}
	if m, err := runner.ParseManifest([]byte("name: x\nabi: 2\n")); err != nil || m.ABI != 2 {
		t.Fatalf("abi 2: %d, %v", m.ABI, err)
	}
	if runner.ABIVersion != 1 {
		t.Fatalf("ABIVersion %d", runner.ABIVersion)
	}
}

// A call after Shutdown is refused, and a second Shutdown does nothing.
func TestSwapToolAfterShutdown(t *testing.T) {
	v1 := &fakeTool{name: "v1"}
	s := runner.NewSwapTool(v1)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(context.Background(), nil); err == nil {
		t.Fatal("a call after Shutdown ran")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Swap(&fakeTool{name: "v2"}); err == nil {
		t.Fatal("a swap after Shutdown was accepted")
	}
}
