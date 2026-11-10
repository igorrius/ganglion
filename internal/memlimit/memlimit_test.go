package memlimit_test

import (
	"errors"
	"io/fs"
	"runtime/debug"
	"testing"

	"github.com/igorrius/ganglion/internal/memlimit"
)

func files(m map[string]string) memlimit.ReadFunc {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		configured uint64
		fs         map[string]string
		want       memlimit.Limit
	}{
		{"configured wins", 100 << 20, map[string]string{memlimit.CgroupV2: "268435456\n"}, memlimit.Limit{Bytes: 100 << 20, Source: memlimit.SourceConfig}},
		{"cgroup v2 at 80 percent", 0, map[string]string{memlimit.CgroupV2: "268435456\n"}, memlimit.Limit{Bytes: 214748364, Source: memlimit.SourceCgroup}},
		{"cgroup v2 max means none", 0, map[string]string{memlimit.CgroupV2: "max\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
		{"cgroup v1", 0, map[string]string{memlimit.CgroupV1: "268435456\n"}, memlimit.Limit{Bytes: 214748364, Source: memlimit.SourceCgroup}},
		{"v1 unlimited is a huge number", 0, map[string]string{memlimit.CgroupV1: "9223372036854771712\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
		{"nothing", 0, nil, memlimit.Limit{Source: memlimit.SourceNone}},
		{"garbage", 0, map[string]string{memlimit.CgroupV2: "lots\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
	}
	for _, c := range cases {
		if got := memlimit.Decide("", c.configured, files(c.fs)); got != c.want {
			t.Errorf("%s: Decide = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDecideReadErrorIsNone(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, errors.New("denied") }
	if got := memlimit.Decide("", 0, read); got.Source != memlimit.SourceNone {
		t.Fatalf("got %+v", got)
	}
}

// R-RUN-6, R-CFG-7: a GOMEMLIMIT the operator set is the Go runtime's own
// setting and wins over the file and the cgroup.
func TestOperatorsGOMEMLIMITWins(t *testing.T) {
	got := memlimit.Decide("300MiB", 100<<20, files(map[string]string{memlimit.CgroupV2: "268435456\n"}))
	if got != (memlimit.Limit{Source: memlimit.SourceEnv}) {
		t.Fatalf("Decide = %+v", got)
	}
}

// Apply leaves the runtime's own limit alone and reports it.
func TestApplyKeepsTheOperatorsLimit(t *testing.T) {
	was := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(was) })
	debug.SetMemoryLimit(300 << 20)
	got := memlimit.Apply(memlimit.Limit{Source: memlimit.SourceEnv})
	if got != (memlimit.Limit{Bytes: 300 << 20, Source: memlimit.SourceEnv}) || debug.SetMemoryLimit(-1) != 300<<20 {
		t.Fatalf("Apply = %+v, runtime %d", got, debug.SetMemoryLimit(-1))
	}
}
