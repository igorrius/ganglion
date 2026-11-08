package host

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
)

// shell runs argv as one shell script, for tests: "sleep 1; echo hi". It
// runs outside any sandbox, which is enough to test the runner's own logic;
// the sandbox's own tests cover the confinement.
type shell struct {
	mu    sync.Mutex
	calls int
}

func (s *shell) Command(ctx context.Context, argv []string, dir string, _ ...string) (*exec.Cmd, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(argv, " "))
	cmd.Dir = dir
	return cmd, nil
}

// started is how many commands the shell made.
func (s *shell) started() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// inbox records the events the runner puts.
type inbox struct {
	mu  sync.Mutex
	evs []event.Event
}

func (b *inbox) put(_ context.Context, e event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evs = append(b.evs, e)
	return nil
}

func (b *inbox) events() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.evs...)
}

// gate is the runner's after func, opened by the test by hand: the short
// wait of a call and the announcing pass each end when the test says so,
// never by sleeping. They have a channel each, so opening one never ends
// the other.
type gate struct{ wait, pass chan time.Time }

func newGate() gate { return gate{wait: make(chan time.Time), pass: make(chan time.Time)} }

func (g gate) after(d time.Duration) <-chan time.Time {
	if d == announceEvery {
		return g.pass
	}
	return g.wait
}
