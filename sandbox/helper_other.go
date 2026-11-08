//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

// Helper is the sandbox's helper; outside Linux there is none, and the
// command never runs.
func Helper() int {
	fmt.Fprintln(os.Stderr, "ganglion sandbox: Linux only")
	return exitSetup
}
