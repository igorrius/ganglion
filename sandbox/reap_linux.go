//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// selfExe is the helper's own image, which it starts again as the confined
// helper.
const selfExe = "/proc/self/exe"

// supervise is the helper of a confined command (ADR-0064). It makes itself
// a child subreaper, so every process the command leaves behind, one that
// called setsid included, becomes its child when its own parent ends; it
// starts the helper that confines itself and execs the command; and when that
// ends, or this process is sent SIGTERM, it kills every child it has until
// none is left. The node's death reaches it as SIGTERM too. It stays outside
// the command's Landlock domain, which from ABI 6 scopes signals, so the
// command cannot kill it to get away. It returns the command's exit code.
func supervise(sp spec) int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	if err := becomeReaper(); err != nil {
		fmt.Fprintln(os.Stderr, "ganglion sandbox:", err)
		return exitSetup
	}
	sp.Reap = false
	child, err := start(sp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ganglion sandbox:", err)
		return exitSetup
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err = <-done:
	case <-stop:
		_ = child.Process.Kill()
		err = <-done
	}
	killChildren()
	return exitCode(err)
}

// becomeReaper makes this process the subreaper of what it starts, and
// turns the signal it gets at the node's death from SIGKILL (procgroup.Own)
// into SIGTERM, which it catches to kill the command's processes first.
func becomeReaper() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("subreaper: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0, 0, 0); err != nil {
		return fmt.Errorf("death signal: %w", err)
	}
	return nil
}

// start runs this image again as the helper that confines itself, with this
// process's stdio, in its process group, killed if this process ends first.
func start(sp spec) (*exec.Cmd, error) {
	cmd, err := command(context.Background(), selfExe, sp)
	if err != nil {
		return nil, err
	}
	defer closeSpec(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// killChildren kills this process's children and reaps them until it has
// none: as a subreaper it inherits the children of each one it kills, so
// the whole tree ends, however deep or detached.
func killChildren() {
	for {
		for _, pid := range children(os.Getpid()) {
			_ = unix.Kill(pid, unix.SIGKILL)
		}
		var ws unix.WaitStatus
		if _, err := unix.Wait4(-1, &ws, 0, nil); errors.Is(err, unix.ECHILD) {
			return
		}
	}
}

// children are the processes whose parent is pid, read from /proc.
func children(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if parent(child) == pid {
			out = append(out, child)
		}
	}
	return out
}

// parent is pid's parent from /proc/<pid>/stat, or 0 when it cannot be read.
func parent(pid int) int {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The name may hold spaces and ") ": cut at the last one, so a process
	// cannot hide behind its name.
	_, after, ok := strings.CutLast(string(stat), ")")
	if !ok {
		return 0
	}
	fields := strings.Fields(after)
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// exitCode is the command's exit code as a shell gives it: 128 plus the
// signal that killed it, exitSetup when it could not be waited for.
func exitCode(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exit.ExitCode()
	}
	return exitSetup
}
