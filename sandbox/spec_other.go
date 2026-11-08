//go:build !linux

package sandbox

import (
	"errors"
	"os"
)

// errNotLinux is the sandbox asked for outside Linux.
var errNotLinux = errors.New("sandbox: Linux only")

func specFile(spec) (*os.File, error) { return nil, errNotLinux }
