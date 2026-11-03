package runner

import (
	"context"
	"encoding/json"
	"sync"
)

// ABIVersion is the module ABI this core speaks (ADR-0011); a module
// whose manifest names another is refused at install (ADR-0041).
const ABIVersion = 1

// Swappable is a tool module a SwapTool can hold: *LazyTool and
// *ToolModule are.
type Swappable interface {
	Call(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)
	Shutdown(ctx context.Context) error
}

// SwapTool is a tool module that can be replaced while it runs
// (R-RUN-8): a call goes to the version current when it starts, and a
// replaced version is shut down once its calls in flight have ended.
type SwapTool struct {
	mu  sync.Mutex
	cur *version
}

type version struct {
	tool     Swappable
	inFlight sync.WaitGroup
}

// NewSwapTool starts with first.
func NewSwapTool(first Swappable) *SwapTool {
	return &SwapTool{cur: &version{tool: first}}
}

// Call runs the call on the current version.
func (s *SwapTool) Call(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	v := s.cur
	v.inFlight.Add(1)
	s.mu.Unlock()
	defer v.inFlight.Done()
	return v.tool.Call(ctx, payload)
}

// Swap makes next current at once. The old version is shut down in the
// background after its calls in flight end; each call is bounded by the
// module's own timeout, so that wait is too.
func (s *SwapTool) Swap(next Swappable) error {
	s.mu.Lock()
	old := s.cur
	s.cur = &version{tool: next}
	s.mu.Unlock()
	go func() {
		old.inFlight.Wait()
		_ = old.tool.Shutdown(context.Background())
	}()
	return nil
}

// Shutdown frees the current version.
func (s *SwapTool) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	v := s.cur
	s.mu.Unlock()
	v.inFlight.Wait()
	return v.tool.Shutdown(ctx)
}
