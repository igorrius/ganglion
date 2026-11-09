//go:build linux

package host

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// ReadMachine reads /proc/meminfo, /proc/loadavg and /proc/uptime.
func ReadMachine() Machine {
	m := Machine{CPUs: runtime.NumCPU()}
	mem, err1 := os.ReadFile("/proc/meminfo")
	load, err2 := os.ReadFile("/proc/loadavg")
	up, err3 := os.ReadFile("/proc/uptime")
	if err1 != nil || err2 != nil || err3 != nil {
		return m
	}
	var ok1, ok2, ok3 bool
	m.MemTotal, m.MemAvailable, ok1 = parseMeminfo(mem)
	m.Load, ok2 = parseLoadavg(load)
	m.Uptime, ok3 = parseUptime(up)
	m.OK = ok1 && ok2 && ok3
	return m
}

// Disk reads the free and total space of the disk holding path.
func Disk(path string) DiskSpace {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskSpace{Path: path}
	}
	bsize := uint64(st.Bsize) //nolint:gosec // a block size is positive
	return DiskSpace{Path: path, Free: st.Bavail * bsize, Total: st.Blocks * bsize, OK: true}
}
