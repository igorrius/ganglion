package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/internal/procgroup"
)

// RunConfig bounds a script run.
type RunConfig struct {
	MaxIn, MaxOut, MaxErr int                              // bytes of stdin, stdout and stderr
	TZ                    string                           // the node's zone, the script's TZ
	Env                   func(name string) (string, bool) // the node's environment (os.LookupEnv)
}

// DefaultRunConfig is 16 KiB in, 64 KiB out, 8 KiB of errors, UTC and
// the process environment.
func DefaultRunConfig() RunConfig {
	return RunConfig{MaxIn: 16 << 10, MaxOut: 64 << 10, MaxErr: 8 << 10, TZ: "UTC", Env: os.LookupEnv}
}

// Runner runs skill scripts (ADR-0042): no shell, a fresh working
// directory, an environment built from scratch, its own process group
// killed whole at the timeout, capped output. It does not confine what
// the script reads on disk: the sandbox of ADR-0039 does, in M3d.
type Runner struct{ cfg RunConfig }

// NewRunner returns a runner.
func NewRunner(cfg RunConfig) *Runner { return &Runner{cfg: cfg} }

// Output is what a script printed and how it ended.
type Output struct {
	Stdout, Stderr string
	Truncated      bool // stdout went past MaxOut
	Exit           int
}

// ErrTimeout is a script that ran past its timeout and was killed. A
// script can leave its process group with setsid; only the sandbox of
// ADR-0039 (M3d) holds it then. Under PID 1 without an init, the
// children killed after a run stay zombies: run the node under one.
var ErrTimeout = errors.New("skills: the script ran past its timeout and was stopped")

// Run runs sk's script with input on stdin. A non-zero exit is an
// Output, not an error; errors are a refused input, a script that could
// not start, and the timeout.
func (r *Runner) Run(ctx context.Context, sk Skill, input []byte) (Output, error) {
	if sk.Script == nil || sk.Dir == "" {
		return Output{}, fmt.Errorf("skills: %s has no script", sk.Name)
	}
	if len(input) > r.cfg.MaxIn {
		return Output{}, fmt.Errorf("skills: input of %d bytes is over %d", len(input), r.cfg.MaxIn)
	}
	if err := checkScript(sk.Dir, *sk.Script, nil); err != nil { // the file may have changed since the start
		return Output{}, fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	work, err := os.MkdirTemp("", "ganglion-skill-")
	if err != nil {
		return Output{}, fmt.Errorf("skills: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	ctx, cancel := context.WithTimeout(ctx, sk.Script.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(sk.Dir, sk.Script.Run))
	cmd.Dir, cmd.Env, cmd.Stdin = work, r.env(sk, work), bytes.NewReader(input)
	stdout, stderr := &capped{max: r.cfg.MaxOut}, &capped{max: r.cfg.MaxErr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	procgroup.Own(cmd)
	err = cmd.Run()
	procgroup.Kill(cmd) // children that outlived the script
	out := Output{Stdout: stdout.b.String(), Stderr: stderr.b.String(), Truncated: stdout.cut}
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return out, ErrTimeout
	case ctx.Err() != nil:
		return out, ctx.Err()
	case errors.Is(err, exec.ErrWaitDelay): // the script exited; a child held its output and was killed
		return out, nil
	case errors.As(err, &exit):
		out.Exit = exit.ExitCode()
		return out, nil
	case err != nil:
		return out, fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	return out, nil
}

// env is built from scratch: a fixed PATH, HOME as the working
// directory, LANG and the node's TZ, then the names the skill lists that
// the node has. Load has refused any secret-looking name.
func (r *Runner) env(sk Skill, home string) []string {
	vars := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home, "LANG": "C.UTF-8", "TZ": r.cfg.TZ}
	for _, name := range sk.Script.Env {
		if v, ok := r.cfg.Env(name); ok {
			vars[name] = v
		}
	}
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

// capped keeps the first max bytes written and drops the rest.
type capped struct {
	b   bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room < len(p) {
		c.cut = true
		c.b.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.b.Write(p)
}
