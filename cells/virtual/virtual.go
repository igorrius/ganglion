// Package virtual is a cells.Backend without a device: reported follows
// desired, optionally after a delay. It serves tests, shadow mode and the
// simulator, and can inject failures and manual changes.
package virtual

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/igorrius/ganglion/cells"
)

const source = "backend:virtual"

// Backend is the virtual backend.
type Backend struct {
	delay time.Duration

	mu      sync.Mutex
	failN   int
	failErr error
}

// Option configures a Backend.
type Option func(*Backend)

// WithDelay makes the backend report only after d, like a slow device.
func WithDelay(d time.Duration) Option {
	return func(b *Backend) { b.delay = d }
}

// New returns a virtual backend.
func New(opts ...Option) *Backend {
	b := &Backend{}
	for _, o := range opts {
		o(b)
	}
	return b
}

// FailNext makes the next n Apply calls fail with err.
func (b *Backend) FailNext(n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failN, b.failErr = n, err
}

// Apply implements cells.Backend.
func (b *Backend) Apply(ctx context.Context, c cells.Cell, r cells.Reporter) error {
	b.mu.Lock()
	if b.failN > 0 {
		b.failN--
		err := b.failErr
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()

	if b.delay == 0 {
		_, err := r.Report(ctx, c.ID, c.Desired, cells.CauseCommanded, source)
		return err
	}
	v := c.Desired
	time.AfterFunc(b.delay, func() {
		// The caller's context may be gone by now; the device would still
		// have switched.
		_, _ = r.Report(context.Background(), c.ID, v, cells.CauseCommanded, source)
	})
	return nil
}

// Flip changes the state from outside, as a person would by hand.
func (b *Backend) Flip(ctx context.Context, r cells.Reporter, id string, v json.RawMessage) error {
	_, err := r.Report(ctx, id, v, cells.CauseExternal, source+":manual")
	return err
}
