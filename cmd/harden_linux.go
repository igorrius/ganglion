//go:build linux

package cmd

import "syscall"

// hardenProcess marks the node as not dumpable: another process of the same
// user, such as a skill's script, cannot read its environment or memory from
// /proc. Without this the secrets passed as environment variables, which a
// script's own environment never holds, are one file away (ADR-0042). It also
// turns off core dumps, which would hold the same secrets.
func hardenProcess() error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
