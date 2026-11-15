//go:build unix

package cmd

import (
	"os"
	"syscall"
)

// execBinary replaces this process with the binary at path, with the same
// arguments and environment. It returns only when that failed; EACCES on a
// data volume mounted noexec is one such failure.
func execBinary(path string) error {
	return syscall.Exec(path, os.Args, os.Environ())
}
