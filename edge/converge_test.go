package edge

import (
	"context"
	"encoding/json"
	"strings"
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

// A fetch that stalls ends at FetchTimeout: the converger goes on, and the
// edge says the files could not be fetched; nothing is kept refused, so the
// next watch tries again.
func TestConvergerStopsAStalledFetch(t *testing.T) {
	var said []byte
	c := &Converger{
		Edge: "e1",
		Fetch: func(ctx context.Context, _, _ string, _ int64) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
		Report:       func(_ context.Context, _, _ string, v []byte) error { said = v; return nil },
		Store:        emptyStore{},
		FetchTimeout: 50 * time.Millisecond,
	}
	v, _ := json.Marshal(Desired{V: DeliveryVersion, Module: "probe", Version: "3", SHA256: strings.Repeat("ab", 32)})
	done := make(chan struct{})
	go func() {
		c.Apply(context.Background(), Update{Edge: "e1", Module: "probe", Value: v})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled fetch blocked the converger")
	}
	var a Actual
	if err := json.Unmarshal(said, &a); err != nil || a.Result != modstore.EdgeRefused || !strings.Contains(a.Reason, "cannot be fetched") {
		t.Fatalf("actual %s (%v)", said, err)
	}
	if c.wasRefused(context.Background(), "probe", a.Desired) {
		t.Fatal("a fetch that timed out is kept as a refusal")
	}
}
