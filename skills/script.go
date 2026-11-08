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
	"github.com/igorrius/ganglion/sandbox"
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

// ErrScriptsOff is a script that cannot run because the command sandbox is
// off: scripts run only confined (ADR-0039, M3d task 3.26).
var ErrScriptsOff = errors.New("skills: scripts are off: the command sandbox is off on this node")

// Starter makes a command confined by the sandbox (sandbox.Sandbox).
type Starter interface {
	Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error)
}

// Runner runs skill scripts (ADR-0042) in the command sandbox (ADR-0039): no
// shell, a fresh working directory holding a copy of the skill's folder
// (the sandbox denies the workspace, where skills live), an environment
// built from scratch, its own process group killed whole at the timeout,
// capped output.
type Runner struct {
	cfg   RunConfig
	start Starter
}

// NewRunner returns a runner whose scripts start through start.
func NewRunner(cfg RunConfig, start Starter) *Runner { return &Runner{cfg: cfg, start: start} }

// A skill's folder is copied for every run, so its size is bounded.
const (
	maxSkillBytes = 4 << 20
	maxSkillFiles = 200
)

var errSkillTooBig = errors.New("skills: the skill's folder is over 4 MiB or 200 files, too big to copy for a run")

// copySkill copies dir into dst. Symlinks are refused by os.CopyFS.
func copySkill(dir, dst string) error {
	var files int
	var size int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		size += info.Size()
		if files > maxSkillFiles || size > maxSkillBytes {
			return errSkillTooBig
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.CopyFS(dst, os.DirFS(dir))
}

// Output is what a script printed and how it ended.
type Output struct {
	Stdout, Stderr string
	Truncated      bool // stdout went past MaxOut
	Exit           int
}

// ErrTimeout is a script that ran past its timeout and was killed. Under PID 1 without an init, the
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
	if err := copySkill(sk.Dir, filepath.Join(work, "skill")); err != nil {
		return Output{}, fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, sk.Script.Timeout)
	defer cancel()
	cmd, err := r.start.Command(ctx, []string{filepath.Join(work, "skill", sk.Script.Run)}, work, r.env(sk, work)...)
	if errors.Is(err, sandbox.ErrOff) {
		return Output{}, ErrScriptsOff
	}
	if err != nil {
		return Output{}, fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	stdout, stderr := &capped{max: r.cfg.MaxOut}, &capped{max: r.cfg.MaxErr}
	err = startAndWait(cmd, input, stdout, stderr)
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

// startAndWait runs cmd with input on stdin and the capped outputs, in a
// process group of its own, then kills what is left of the group.
func startAndWait(cmd *exec.Cmd, input []byte, stdout, stderr *capped) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), stdout, stderr
	cmd.WaitDelay = time.Second
	procgroup.Own(cmd)
	err := cmd.Start()
	sandbox.Release(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	procgroup.Kill(cmd) // children that outlived the script
	return err
}

// env is what the script gets on top of the sandbox's environment (PATH,
// LANG and the rest): HOME as the working directory, the node's TZ, then the
// names the skill lists that the node has. Load has refused any
// secret-looking name.
func (r *Runner) env(sk Skill, home string) []string {
	vars := map[string]string{"HOME": home, "TZ": r.cfg.TZ}
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
