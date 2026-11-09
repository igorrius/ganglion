// Package memlimit decides the node's soft memory limit (R-RUN-6,
// ADR-0056): the operator's own GOMEMLIMIT, else the configured value, else
// about 80 % of the container's limit, else none.
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
	SourceEnv    Source = "env"
	SourceConfig Source = "config"
	SourceCgroup Source = "cgroup"
	SourceNone   Source = "none"
)

// Limit is the decided limit; Bytes is 0 when Source is SourceNone, and
// for SourceEnv until Apply reads it from the runtime.
type Limit struct {
	Bytes  int64
	Source Source
}

// ReadFunc reads a file, as os.ReadFile does.
type ReadFunc func(path string) ([]byte, error)

// Decide returns SourceEnv when the operator set GOMEMLIMIT (env is its
// value; the Go runtime reads it itself, like OTEL_* for OpenTelemetry),
// else the configured bytes when set, else 80 % of the cgroup limit, else
// none. A file it cannot read or parse counts as no limit.
func Decide(env string, configured uint64, read ReadFunc) Limit {
	if env != "" {
		return Limit{Source: SourceEnv}
	}
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

// Apply sets the limit on the runtime and returns it. It leaves the
// runtime alone for none, and for the operator's GOMEMLIMIT, whose bytes it
// reads back from the runtime.
func Apply(l Limit) Limit {
	switch l.Source {
	case SourceNone:
	case SourceEnv:
		l.Bytes = debug.SetMemoryLimit(-1)
	default:
		debug.SetMemoryLimit(l.Bytes)
	}
	return l
}
