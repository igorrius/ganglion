package size_test

import (
	"testing"

	"github.com/igorrius/ganglion/internal/size"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]uint64{"0": 0, "512": 512, "64KiB": 64 << 10, "100MiB": 100 << 20, "1GiB": 1 << 30} {
		got, err := size.Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MiB", "-1MiB", "1.5MiB", "10MB", "1TiB"} {
		if _, err := size.Parse(bad); err == nil {
			t.Errorf("Parse(%q): want an error", bad)
		}
	}
}
