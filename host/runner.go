package host

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/procgroup"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Starter makes a command confined by the sandbox (sandbox.Sandbox); it
// fails when the sandbox is off, and the command does not run.
type Starter interface {
	Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error)
}

// PutFunc stores an event in the inbox; a stored IdemKey is a no-op.
type PutFunc func(ctx context.Context, e event.Event) error

// Limits bound commands (R-CMD-4).
type Limits struct {
	Wait       time.Duration // how long a call waits; past it the command goes on in the background
	Max        time.Duration // the hard time limit; a call may ask for less, never more
	Parallel   int           // commands that run at once; others wait for a slot
	Head, Tail int           // output bytes kept from the start and the end
}

// DefaultLimits are the M3d design's (section 4.3).
func DefaultLimits() Limits {
	return Limits{Wait: 20 * time.Second, Max: 15 * time.Minute, Parallel: 2, Head: 6 << 10, Tail: 6 << 10}
}

// announceEvery is how often the runner stores the events of background
// commands that ended, and prunes old rows.
const announceEvery = 30 * time.Second

// keepJobs is how long a finished row stays: a rerun within it still finds
// its outcome.
const keepJobs = 7 * 24 * time.Hour

// waitDelay is how long a command's output may stay open after it exited
// before the runner stops reading it.
const waitDelay = 2 * time.Second

// ErrNoJob is a key the runner has no row for.
var ErrNoJob = errors.New("host: no such command")

// errStopping is a call that came after the runner stopped.
var errStopping = errors.New("host: the runner is stopping")

// Request is one command of one call.
type Request struct {
	Key                        string // the call's key: the command runs once per key (R-DLV-6)
	Conv, Transport, Principal string
	Depth                      int // the call's depth; a resumed turn runs one deeper (R-AGT-4)
	Argv                       []string
	Dir                        string
	Timeout                    time.Duration // 0 is Limits.Max
}

// JobState is where a command stands.
type JobState string

// States.
const (
	JobRunning     JobState = "running"
	JobDone        JobState = "done"
	JobInterrupted JobState = "interrupted" // the node stopped while it ran
)

// Job is a command's row.
type Job struct {
	Key, Conv, Transport, Principal string
	Depth                           int
	Argv                            []string
	Dir                             string
	State                           JobState
	Background                      bool
	Text                            string // the outcome as the model reads it, redacted; "" while running
	Started, Ended                  time.Time
}

// Answer is what a call gets: the job, and whether its end is told here.
// A job whose end is not told here ends as a command.done event, so each
// end is told once: here or there. Callers branch on Done only: a not-Done
// answer's Job may already read done, when it ended in the background.
type Answer struct {
	Job  Job
	Done bool
}

// outcome is how a command ended, the label set of ganglion.host.commands.
type outcome string

// Outcomes.
const (
	outcomeOK          outcome = "ok"
	outcomeFailed      outcome = "failed"
	outcomeTimeout     outcome = "timeout"
	outcomeNotRun      outcome = "not_run"
	outcomeInterrupted outcome = "interrupted"
)

// Runner runs host commands in the sandbox (R-CMD-1, R-CMD-3, R-CMD-4). It is
// a node component: commands live under its context, not the turn's, and
// Run returns only after they ended.
type Runner struct {
	db    *sql.DB
	start Starter
	put   PutFunc
	lim   Limits
	slots chan struct{}
	hide  func(string) string
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	log   *slog.Logger
	base  chan context.Context // holds the running component's context once
	tel   runnerTelemetry

	mu      sync.Mutex // guards stopped and the calls to cmds.Add
	stopped bool
	cmds    sync.WaitGroup // the commands' goroutines, which Run waits for
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithRedact sets how output loses secrets; the default keeps it as is, so
// config always passes redact.String with the node's secret values.
func WithRedact(f func(string) string) RunnerOption { return func(r *Runner) { r.hide = f } }

// WithRunnerClock sets the clock and the waits, for tests.
func WithRunnerClock(now func() time.Time, after func(time.Duration) <-chan time.Time) RunnerOption {
	return func(r *Runner) { r.now, r.after = now, after }
}

// WithRunnerLog sets the logger.
func WithRunnerLog(l *slog.Logger) RunnerOption { return func(r *Runner) { r.log = l } }

// NewRunner migrates the "host" component and returns the runner.
func NewRunner(ctx context.Context, db *sql.DB, start Starter, put PutFunc, lim Limits, opts ...RunnerOption) (*Runner, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "host", sub); err != nil {
		return nil, err
	}
	if lim.Parallel < 1 || lim.Max <= 0 || lim.Wait <= 0 {
		return nil, fmt.Errorf("host: limits %+v: a slot, a time limit and a wait are needed", lim)
	}
	r := &Runner{db: db, start: start, put: put, lim: lim, slots: make(chan struct{}, lim.Parallel),
		hide: func(s string) string { return s }, now: time.Now, after: time.After,
		log: slog.New(slog.DiscardHandler), base: make(chan context.Context, 1)}
	for _, o := range opts {
		o(r)
	}
	r.tel = newRunnerTelemetry(r.slots)
	return r, nil
}

// Name implements node.Component.
func (*Runner) Name() string { return "host-commands" }

// Run marks the commands a stopped node left as interrupted, then serves
// commands until ctx ends, storing the events of background commands that
// ended and pruning old rows every announceEvery. At the end the running
// commands are killed with ctx, and Run returns once their ends are stored.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.interrupt(ctx); err != nil {
		return err
	}
	r.base <- ctx
	defer r.stop()
	for {
		r.pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-r.after(announceEvery):
		}
	}
}

// stop takes no more commands and waits for the running ones to end.
func (r *Runner) stop() {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.cmds.Wait()
}

// enter counts a command Run must wait for; false once the runner stopped.
func (r *Runner) enter() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.cmds.Add(1)
	return true
}

// Exec runs the request's command once per key. A command that ends within
// Limits.Wait is answered done; one still running then goes on, and its end
// comes as a command.done event in its conversation. A key seen before
// answers with its row: done or interrupted as stored, or still running.
func (r *Runner) Exec(ctx context.Context, q Request) (Answer, error) {
	base, err := r.context(ctx)
	if err != nil {
		return Answer{}, err
	}
	if !r.enter() {
		return Answer{}, errStopping
	}
	j, fresh, err := r.begin(ctx, q)
	if err != nil || !fresh {
		r.cmds.Done()
		return r.seen(ctx, j, err)
	}
	ended := make(chan Job, 1)
	go func() {
		defer r.cmds.Done()
		r.execute(base, q, ended)
	}()
	select {
	case j := <-ended:
		return Answer{Job: j, Done: !j.Background}, nil
	case <-r.after(r.lim.Wait):
	case <-ctx.Done():
	}
	return r.detach(context.WithoutCancel(ctx), q.Key, ended)
}

// context is the component's context, once Run has started.
func (r *Runner) context(ctx context.Context) (context.Context, error) {
	select {
	case base := <-r.base:
		r.base <- base
		return base, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("host: the runner has not started: %w", ctx.Err())
	}
}

// seen answers a key that has a row: done or interrupted, or still running,
// which its call learns as running in the background. A row that ended in
// the background is not done: its event tells the end.
func (r *Runner) seen(ctx context.Context, j Job, err error) (Answer, error) {
	if err != nil {
		return Answer{}, err
	}
	if j.State != JobRunning {
		return Answer{Job: j, Done: !j.Background}, nil
	}
	return r.detach(ctx, j.Key, nil)
}

// detach marks a running command background, so its end is announced. A
// command that ended first is answered from its row: done, unless another
// call of its key sent it to the background first, when the event tells
// its end.
func (r *Runner) detach(ctx context.Context, key string, ended <-chan Job) (Answer, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE host_jobs SET background = 1 WHERE key = ? AND state = 'running'`, key)
	if err != nil {
		return Answer{}, fmt.Errorf("host: detach %s: %w", key, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		j, err := r.Get(ctx, key)
		return Answer{Job: j}, err
	}
	if ended != nil {
		j := <-ended
		return Answer{Job: j, Done: !j.Background}, nil
	}
	j, err := r.Get(ctx, key)
	return Answer{Job: j, Done: !j.Background}, err
}

// execute runs the command and stores how it ended, then hands the row to
// the call, which may have gone on: ended holds one, so this never blocks.
func (r *Runner) execute(base context.Context, q Request, ended chan<- Job) {
	text, how := r.runOne(base, q)
	ctx := context.WithoutCancel(base)
	state := JobDone
	if how == outcomeInterrupted {
		state = JobInterrupted
	}
	j, err := r.end(ctx, q.Key, state, text)
	if err != nil {
		// The waiting call still gets the outcome; a call that already went
		// on never hears of it, and its row stays running until a restart
		// marks it interrupted.
		r.log.Error("host: the end of a command was not stored", "key", q.Key, "err", err)
		ended <- Job{Key: q.Key, Argv: q.Argv, Dir: q.Dir, State: state, Text: text}
		return
	}
	r.tel.ended(ctx, how, j.Ended.Sub(j.Started))
	ended <- j
}

// runOne waits for a slot, then runs the command in its own process group
// under its time limit, and returns the text the model reads. It gets no
// input: stdin is /dev/null, so a program that asks reads the end at once.
func (r *Runner) runOne(base context.Context, q Request) (string, outcome) {
	select {
	case r.slots <- struct{}{}:
	case <-base.Done():
		return "not run: the node is stopping", outcomeNotRun
	}
	defer func() { <-r.slots }()
	limit := r.lim.Max
	if q.Timeout > 0 && q.Timeout < limit {
		limit = q.Timeout
	}
	ctx, cancel := context.WithTimeout(base, limit)
	defer cancel()
	cmd, err := r.start.Command(ctx, q.Argv, q.Dir)
	if err != nil {
		return "not run: " + r.hide(err.Error()), outcomeNotRun
	}
	out := newCapture(r.lim.Head, r.lim.Tail)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = out, out, waitDelay
	procgroup.Own(cmd)
	began := r.now()
	err = cmd.Start()
	sandbox.Release(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	procgroup.Kill(cmd)
	return r.render(ctx, cmd, err, limit, r.now().Sub(began), out)
}

// render is the text the model reads of an ended command: a first line of
// how it ended and the size of its output, then the output, valid UTF-8 and
// redacted (capture.text).
func (r *Runner) render(ctx context.Context, cmd *exec.Cmd, err error, limit, took time.Duration, out *capture) (string, outcome) {
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var head string
	var how outcome
	switch {
	case cmd.Process == nil:
		return "not run: " + r.hide(err.Error()), outcomeNotRun
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		head, how = fmt.Sprintf("stopped after %s: past its time limit (exit %d)", limit, code), outcomeTimeout
	case ctx.Err() != nil: // the node is stopping
		return interruptedText, outcomeInterrupted
	case code == 0:
		head, how = fmt.Sprintf("exit 0 after %s", took.Round(time.Millisecond)), outcomeOK
	default:
		head, how = fmt.Sprintf("exit %d after %s", code, took.Round(time.Millisecond)), outcomeFailed
	}
	text, cut := out.text(r.hide)
	head += fmt.Sprintf("; %d bytes of output", out.size())
	if cut {
		head += ", the middle left out"
	}
	return head + "\n" + text, how
}
