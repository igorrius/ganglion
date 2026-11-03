//go:build linux || darwin

package claudecli

import (
	"os/exec"
	"syscall"
)

// ownGroup runs cmd in a process group of its own and makes a cancelled
// context kill the whole group, so the relay claude started does not
// outlive it (ADR-0036).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
