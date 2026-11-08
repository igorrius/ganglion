//go:build unix

// Package procgroup runs a child in a process group of its own, so a time
// limit or the end of its work kills everything it started (R-CMD-4,
// ADR-0042). Shared by skill scripts, host commands and the claude CLI.
package procgroup

import (
	"os/exec"
	"syscall"
)

// Own puts cmd in a process group of its own; a cancelled context kills the
// whole group. On Linux the child also dies with the node (deathSignal).
func Own(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	deathSignal(cmd.SysProcAttr)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// Kill kills what is left of the group after the child ended.
func Kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
