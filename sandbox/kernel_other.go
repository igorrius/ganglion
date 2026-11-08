//go:build !linux

package sandbox

import "os"

type hostKernel struct{}

// HostKernel is the running process; outside Linux the sandbox is off
// before it is asked anything (New checks the OS first).
func HostKernel() Kernel { return hostKernel{} }

func (hostKernel) Euid() int                 { return os.Geteuid() }
func (hostKernel) LandlockABI() (int, error) { return 0, errNotLinux }
func (hostKernel) Dumpable() (bool, error)   { return true, errNotLinux }
