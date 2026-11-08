package host

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
	"time"
)

// Machine is what the kernel says of memory, load and uptime; OK is false
// where it cannot be read (not Linux).
type Machine struct {
	MemTotal, MemAvailable uint64
	Load                   [3]float64
	CPUs                   int
	Uptime                 time.Duration
	OK                     bool
}

// parseMeminfo reads MemTotal and MemAvailable, in kB, as bytes.
func parseMeminfo(b []byte) (total, avail uint64, ok bool) {
	var seen int
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found || (name != "MemTotal" && name != "MemAvailable") {
			continue
		}
		kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			return 0, 0, false
		}
		if name == "MemTotal" {
			total = kb * 1024
		} else {
			avail = kb * 1024
		}
		seen++
	}
	return total, avail, seen == 2
}

// parseLoadavg reads the three load averages.
func parseLoadavg(b []byte) ([3]float64, bool) {
	var out [3]float64
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return out, false
	}
	for i := range out {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return out, false
		}
		out[i] = v
	}
	return out, true
}

// parseUptime reads the seconds since boot.
func parseUptime(b []byte) (time.Duration, bool) {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(s * float64(time.Second)), true
}

// DiskSpace is the free and total bytes of the disk holding Path; OK is
// false where they cannot be read.
type DiskSpace struct {
	Path        string
	Free, Total uint64
	OK          bool
}
