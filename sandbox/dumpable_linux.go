//go:build linux

package sandbox

import "golang.org/x/sys/unix"

// SetDumpable sets whether this process's /proc files are open to its own
// user. The node clears it at start (Harden): another process of the same
// user, such as a skill's script, cannot then read its environment or memory,
// and the process leaves no core dump holding the same secrets (ADR-0042).
func SetDumpable(on bool) error {
	v := uintptr(0)
	if on {
		v = 1
	}
	return unix.Prctl(unix.PR_SET_DUMPABLE, v, 0, 0, 0)
}
