//go:build linux || darwin

package skills

import (
	"os/exec"
	"syscall"
)

// ownGroup runs cmd in a process group of its own; a cancelled context
// kills the whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// killGroup kills what is left of the group after the script ended.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
