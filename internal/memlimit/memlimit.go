// Package memlimit decides the node's soft memory limit (R-RUN-6,
// ADR-0056): the configured value, else about 80 % of the container's
// limit, else none.
package memlimit

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// The cgroup files that hold the container's memory limit.
const (
	CgroupV2 = "/sys/fs/cgroup/memory.max"
	CgroupV1 = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

// share is the part of the container's limit the heap may use.
const share = 0.8

// unlimitedV1 is above any real limit: cgroup v1 writes a page-aligned
// maximum when there is none.
const unlimitedV1 = 1 << 62

// Source tells where a limit came from.
type Source string

// Sources.
const (
	SourceConfig Source = "config"
	SourceCgroup Source = "cgroup"
	SourceNone   Source = "none"
)

// Limit is the decided limit; Bytes is 0 when Source is SourceNone.
type Limit struct {
	Bytes  int64
	Source Source
}

// ReadFunc reads a file, as os.ReadFile does.
type ReadFunc func(path string) ([]byte, error)

// Decide returns the configured bytes when set, else 80 % of the cgroup
// limit, else none. A file it cannot read or parse counts as no limit.
func Decide(configured uint64, read ReadFunc) Limit {
	if configured > 0 {
		return Limit{Bytes: int64(configured), Source: SourceConfig}
	}
	for _, p := range []string{CgroupV2, CgroupV1} {
		if n, ok := cgroup(read, p); ok {
			return Limit{Bytes: int64(float64(n) * share), Source: SourceCgroup}
		}
	}
	return Limit{Source: SourceNone}
}

func cgroup(read ReadFunc, path string) (uint64, bool) {
	b, err := read(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n == 0 || n >= unlimitedV1 {
		return 0, false
	}
	return n, true
}

// Apply sets the limit on the runtime unless it is none, and returns it.
func Apply(l Limit) Limit {
	if l.Source != SourceNone {
		debug.SetMemoryLimit(l.Bytes)
	}
	return l
}
