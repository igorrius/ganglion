//go:build unix && !linux

package procgroup

import "syscall"

// deathSignal does nothing where the kernel has no parent-death signal: the
// group kill on a time limit is all there is.
func deathSignal(*syscall.SysProcAttr) {}
