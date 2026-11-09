//go:build unix

package agent

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// heldStarter runs every command as a shell that prints what the test writes
// to its pipe and ends when the test closes it, so the test decides when a
// command ends, without sleeping. Starting one ends the call's context
// (cancel), as a turn whose clock ran out while its command waited would.
type heldStarter struct {
	r      *os.File
	cancel context.CancelFunc
}

func (s *heldStarter) Command(ctx context.Context, _ []string, dir string, _ ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "cat <&3")
	cmd.Dir, cmd.ExtraFiles = dir, []*os.File{s.r}
	s.cancel()
	return cmd, nil
}

// putBox is the inbox the runner puts its events in.
type putBox struct {
	mu  sync.Mutex
	evs []event.Event
}

func (b *putBox) put(_ context.Context, e event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evs = append(b.evs, e)
	return nil
}

func (b *putBox) events() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.evs)
}

type openGate struct{}

func (openGate) Open() bool { return true }

type nothingIgnored struct{}

func (nothingIgnored) Ignored(context.Context, string, []string) (string, bool) { return "", false }

// The Task 4 review's case end to end, with the real runner and host.exec: a
// call whose context ends while its command runs leaves the command in the
// background, the turn lists it, and its end comes as a command.done event
// that resumes the conversation with the outcome.
func TestACancelledCallsCommandEndsAsAResumedTurn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	callCtx := make(chan context.CancelFunc, 1)
	starter := &heldStarter{r: r, cancel: func() { (<-callCtx)() }}
	pass, never := make(chan time.Time), make(<-chan time.Time)
	const wait = time.Hour // the short wait never ends here: only the call's context does
	after := func(d time.Duration) <-chan time.Time {
		if d == wait {
			return never
		}
		return pass
	}
	e := newEnv(t, Config{}, nil)
	box := &putBox{}
	runner, err := host.NewRunner(t.Context(), e.db, starter, box.put,
		host.Limits{Wait: wait, Max: time.Minute, Parallel: 1, Head: 1 << 10, Tail: 1 << 10}, host.WithRunnerClock(time.Now, after))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	e.deps.Tools = registry(t, builtin.NewHostExec(runner, nothingIgnored{}, openGate{}, "/"))
	e.deps.Commands = runner
	// The call's own context, which the starter ends once the command started.
	e.deps.Middleware = []tool.Middleware{func(next tool.Func) tool.Func {
		return func(ctx context.Context, c tool.Call) (tool.Result, error) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			callCtx <- cancel
			return next(ctx, c)
		}
	}}
	e.a = New(overDefaults(Config{}), e.deps)

	e.smart.push(callWith("host.exec", `{"argv":["du","-sh","/home/o/project"],"reason":"its size"}`), say("It runs in the background."))
	ev := message(owner, "Igor", "how big is my project?")
	e.turn(t, ev)
	if st := e.state(t); !slices.Equal(st.Commands, []string{ev.ID + ":1"}) {
		t.Fatalf("commands %v", st.Commands)
	}
	if _, err := w.WriteString("2.1G\t/home/o/project\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	evs := drivePasses(t, pass, box, 1)
	if evs[0].Kind != event.KindCommandDone || evs[0].Conv != "telegram:42" || evs[0].Source != "telegram" {
		t.Fatalf("events %+v", evs)
	}

	e.smart.push(say("~/project is 2.1 GiB."))
	out := e.turn(t, evs[0])
	if reply, ok := findOut(out, isReply); !ok || reply.Text != "~/project is 2.1 GiB." || reply.Conv != "telegram:42" {
		t.Fatalf("out %+v", out)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); !strings.HasPrefix(got, "[command finished] du -sh /home/o/project: <external source=\"host.exec\">\nexit 0") ||
		!strings.Contains(got, "2.1G") {
		t.Fatalf("the model read %q", got)
	}
	if st := e.state(t); len(st.Commands) != 0 || !st.Tainted {
		t.Fatalf("state %+v", st)
	}
}

// drivePasses opens the runner's announcing pass until box holds n events,
// for at most 10 s.
func drivePasses(t *testing.T, pass chan<- time.Time, box *putBox, n int) []event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for len(box.events()) < n {
		select {
		case pass <- time.Now():
		case <-ctx.Done():
			t.Fatalf("%d events after 10 s, want %d", len(box.events()), n)
		}
	}
	return box.events()
}
