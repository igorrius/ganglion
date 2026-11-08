//go:build lab && linux

package main

import "syscall"

// harden marks this process not dumpable, as `ganglion run` does: the lab's
// node runs in it, and the sandbox is off for a process whose environment
// another process of the same user could read (ADR-0039).
func harden() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0) }
