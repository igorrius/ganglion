// Package proc is the node's own lines for /status (R-OBS-11): the version of
// ganglion, how long the process runs, its memory against the soft limit
// (ADR-0056), and the sizes of the database and the logs.
package proc

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// memoryHigh is the share of the soft limit past which the memory line warns.
const memoryHigh = 0.9

// Files are the node's files whose sizes are shown.
type Files struct {
	DB   string // the database; its -wal and -shm files count with it
	Logs string // the log directory; "" when logs are not on disk
}

// MemoryFunc reads the resident and peak memory in bytes; ok is false where it cannot.
type MemoryFunc func() (rss, peak int64, ok bool)

// LimitFunc returns the soft memory limit in bytes; math.MaxInt64 is none.
type LimitFunc func() int64

// Option changes a Source.
type Option func(*Source)

// WithBuild sets what the binary is, for tests.
func WithBuild(in buildinfo.Info) Option { return func(s *Source) { s.build = in } }

// WithMemory sets the memory reader, for tests.
func WithMemory(f MemoryFunc) Option { return func(s *Source) { s.memory = f } }

// WithLimit sets the soft limit's reader, for tests.
func WithLimit(f LimitFunc) Option { return func(s *Source) { s.limit = f } }

// Source is the node's own lines.
type Source struct {
	start  time.Time
	files  Files
	build  buildinfo.Info
	memory MemoryFunc
	limit  LimitFunc
}

// New returns the lines of a process that started at start.
func New(start time.Time, files Files, opts ...Option) *Source {
	s := &Source{start: start, files: files, build: buildinfo.Read(), memory: procMemory, limit: softLimit}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements status.Source.
func (*Source) Name() string { return "node" }

// Status implements status.Source.
func (s *Source) Status(_ context.Context, sc status.Scope) ([]status.Check, error) {
	out := []status.Check{s.version(), {Section: status.SectionNode, State: status.OK, Line: texts.StatusUptime,
		Vars: texts.Vars{"uptime": status.Duration(sc.Now.Sub(s.start))}}}
	if c, ok := s.memoryLine(); ok {
		out = append(out, c)
	}
	return append(out, s.filesLine()), nil
}

// version is ganglion's version line, as `ganglion version` has it.
func (s *Source) version() status.Check {
	in := s.build
	v := texts.Vars{"version": in.Version, "go": in.Go, "platform": in.OS + "/" + in.Arch}
	key := texts.StatusVersionUnknown
	if in.Commit != "" {
		v["commit"], v["date"] = in.Commit[:min(7, len(in.Commit))], in.CommitTime.UTC().Format(time.DateOnly)
		key = texts.StatusVersion
		if in.Modified {
			key = texts.StatusVersionModified
		}
	}
	return status.Check{Section: status.SectionNode, State: status.OK, Line: key, Vars: v}
}

// memoryLine is the resident and peak memory against the soft limit; none where it cannot be read.
func (s *Source) memoryLine() (status.Check, bool) {
	rss, peak, ok := s.memory()
	if !ok {
		return status.Check{}, false
	}
	c := status.Check{Section: status.SectionNode, State: status.OK, Line: texts.StatusMemoryNoLimit,
		Vars: texts.Vars{"rss": status.Bytes(rss), "peak": status.Bytes(peak)}}
	if limit := s.limit(); limit != math.MaxInt64 {
		c.Line, c.Vars["limit"] = texts.StatusMemory, status.Bytes(limit)
		if float64(rss) > memoryHigh*float64(limit) {
			c.State, c.Reason = status.Warn, texts.StatusReasonMemoryHigh
		}
	}
	return c, true
}

// filesLine is the database's size with its journal files, and the logs'.
func (s *Source) filesLine() status.Check {
	var db int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		db += size(s.files.DB + suffix)
	}
	c := status.Check{Section: status.SectionNode, State: status.OK, Line: texts.StatusFilesNoLogs, Vars: texts.Vars{"db": status.Bytes(db)}}
	if s.files.Logs != "" {
		c.Line, c.Vars["logs"] = texts.StatusFiles, status.Bytes(dirSize(s.files.Logs))
	}
	return c
}

// size is a file's size; 0 when it is missing.
func size(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// dirSize sums the regular files directly in dir: rotated logs sit side by side (ADR-0043).
func dirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if e.Type().IsRegular() {
			n += size(filepath.Join(dir, e.Name()))
		}
	}
	return n
}

// procMemory reads this process's VmRSS and VmHWM.
func procMemory() (int64, int64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0, false
	}
	return parseStatus(b)
}

// parseStatus reads VmRSS and VmHWM, in kB, from /proc/<pid>/status.
func parseStatus(b []byte) (rss, peak int64, ok bool) {
	var seen int
	for line := range bytes.Lines(b) {
		name, rest, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		fields := bytes.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(string(fields[0]), 10, 64)
		if err != nil {
			continue
		}
		switch string(name) {
		case "VmRSS":
			rss, seen = kb<<10, seen+1
		case "VmHWM":
			peak, seen = kb<<10, seen+1
		}
	}
	return rss, peak, seen == 2
}

// softLimit is the runtime's soft memory limit, read without changing it.
func softLimit() int64 { return debug.SetMemoryLimit(-1) }
