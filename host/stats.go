package host

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/status/proc"
)

// Report is everything host.stats says.
type Report struct {
	Machine   Machine
	RSS, Peak int64 // the node's resident and peak memory; 0 when not known
	Disks     []DiskSpace
	Places    []Usage
}

// Stats reads the report without a subprocess, so it works where commands
// are off (R-CMD-5).
type Stats struct {
	places  []Place
	disks   []string
	machine func() Machine
	memory  proc.MemoryFunc
	disk    func(string) DiskSpace
}

// StatsOption changes a Stats, for tests.
type StatsOption func(*Stats)

// NewStats reports the machine, the disks of / and dataDir, and places.
func NewStats(places []Place, dataDir string, opts ...StatsOption) *Stats {
	disks := []string{"/"}
	if d := filepath.Clean(dataDir); d != "/" {
		disks = append(disks, d)
	}
	s := &Stats{places: places, disks: disks, machine: ReadMachine, memory: proc.Memory, disk: Disk}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Read gathers the report.
func (s *Stats) Read(context.Context) Report {
	r := Report{Machine: s.machine()}
	if rss, peak, ok := s.memory(); ok {
		r.RSS, r.Peak = rss, peak
	}
	for _, d := range s.disks {
		r.Disks = append(r.Disks, s.disk(d))
	}
	for _, p := range s.places {
		r.Places = append(r.Places, Measure(p))
	}
	return mergeTemp(r)
}

// mergeTemp adds up the temp folder's several prefixes into one line.
func mergeTemp(r Report) Report {
	var temp *Usage
	out := r.Places[:0]
	for _, u := range r.Places {
		if u.Place.Name != PlaceTemp {
			out = append(out, u)
			continue
		}
		if temp == nil {
			u.Place.Prefix = ""
			out = append(out, u)
			temp = &out[len(out)-1]
			continue
		}
		temp.Bytes, temp.Entries, temp.Partial = temp.Bytes+u.Bytes, temp.Entries+u.Entries, temp.Partial || u.Partial
	}
	r.Places = out
	return r
}

// Render is the report as the model reads it, in English.
func Render(r Report) string {
	var b strings.Builder
	m := r.Machine
	if m.OK {
		fmt.Fprintf(&b, "Memory: %s total, %s available.\nLoad: %.2f %.2f %.2f (%d CPUs). Up %s.\n",
			status.Bytes(int64(m.MemTotal)), status.Bytes(int64(m.MemAvailable)), m.Load[0], m.Load[1], m.Load[2], m.CPUs,
			m.Uptime.Truncate(time.Minute))
	} else {
		b.WriteString("Machine memory and load: not known here.\n")
	}
	if r.RSS > 0 {
		fmt.Fprintf(&b, "The node uses %s (peak %s).\n", status.Bytes(r.RSS), status.Bytes(r.Peak))
	}
	b.WriteString("Disk:\n")
	for _, d := range r.Disks {
		if !d.OK {
			fmt.Fprintf(&b, "- %s: not known\n", d.Path)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s free of %s\n", d.Path, status.Bytes(int64(d.Free)), status.Bytes(int64(d.Total)))
	}
	b.WriteString("The node's own files:\n")
	for _, u := range r.Places {
		fmt.Fprintf(&b, "- %s: %s in %d files", u.Place.Name, status.Bytes(u.Bytes), u.Entries)
		if u.Partial {
			b.WriteString(" (at least)")
		}
		if u.Place.MaxAge > 0 {
			fmt.Fprintf(&b, ", cleaned after %s", u.Place.MaxAge)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
