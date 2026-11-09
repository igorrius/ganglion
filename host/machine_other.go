//go:build !linux

package host

import "runtime"

// ReadMachine knows only the CPU count off Linux; OK stays false.
func ReadMachine() Machine { return Machine{CPUs: runtime.NumCPU()} }

// Disk knows nothing off Linux.
func Disk(path string) DiskSpace { return DiskSpace{Path: path} }
