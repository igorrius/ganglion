//go:build unix

package cmd

import (
	"errors"
	"os"
	"syscall"
)

// execBinary replaces this process with the binary at path, with the same
// arguments and environment. It returns only when that failed; EACCES on a
// data volume mounted noexec is one such failure.
func execBinary(path string) error {
	return syscall.Exec(path, os.Args, os.Environ())
}

// fileOwner is the user id that owns the file at path.
func fileOwner(path string) (int, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("update: no owner for " + path)
	}
	return int(st.Uid), nil
}
