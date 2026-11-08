//go:build linux

package sandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

type hostKernel struct{}

// HostKernel is the running kernel and process.
func HostKernel() Kernel { return hostKernel{} }

func (hostKernel) Euid() int                 { return os.Geteuid() }
func (hostKernel) LandlockABI() (int, error) { return landlockABI() }

// Dumpable reports whether this process's /proc files are open to its own
// user; the node clears it at start (cmd/harden_linux.go).
func (hostKernel) Dumpable() (bool, error) {
	v, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	return v != 0, err
}
