package edge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

// emptyStore is an edge's store with no module.
type emptyStore struct{}

func (emptyStore) List(context.Context) ([]modstore.Version, error) { return nil, nil }
func (emptyStore) InstallDelivered(context.Context, modstore.Build, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, nil
}
func (emptyStore) RemoveLocal(context.Context, string) error { return nil }

// The watch starts once the link is up, not before; it starts again when
// the link comes back, and what it hands over is applied.
func TestConvergerWatchesWhileLinked(t *testing.T) {
	var (
		linked  atomic.Bool
		watches atomic.Int32
		mu      sync.Mutex
		stops   int
	)
	c := &Converger{
		Edge: "e1",
		Watch: func(_ context.Context, edge string, fn func(Update)) (func() error, error) {
			watches.Add(1)
			fn(Update{Edge: edge, Module: "probe", Deleted: true})
			return func() error { mu.Lock(); stops++; mu.Unlock(); return nil }, nil
		},
		Linked: func(context.Context) bool { return linked.Load() },
		Store:  emptyStore{},
		Poll:   10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Run(ctx) }()
	wait := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for watches.Load() != n {
			if time.Now().After(deadline) {
				t.Fatalf("watches = %d, want %d", watches.Load(), n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if watches.Load() != 0 {
		t.Fatal("the watch started without a link")
	}
	linked.Store(true)
	wait(1)
	linked.Store(false)
	time.Sleep(50 * time.Millisecond)
	linked.Store(true)
	wait(2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if stops != 2 {
		t.Fatalf("stops = %d, want 2: the old watch goes before a new one, the last at the end", stops)
	}
}
