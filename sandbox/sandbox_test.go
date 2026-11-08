//go:build linux

package sandbox

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestNewIsOffForEachMissingPiece(t *testing.T) {
	cases := map[string]struct {
		k    fakeKernel
		want Reason
	}{
		"root":        {fakeKernel{euid: 0, abi: 5}, Root},
		"no landlock": {fakeKernel{euid: 1000, abiErr: errors.New("ENOSYS")}, NoLandlock},
		"dumpable":    {fakeKernel{euid: 1000, abi: 5, dumpable: true}, Dumpable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := New("/nonexistent", Policy{}, c.k, time.Unix(0, 0))
			if ok, r := s.Ready(); ok || r != c.want {
				t.Fatalf("Ready() = %v, %v; want false, %v", ok, r, c.want)
			}
			if _, err := s.Command(t.Context(), []string{"true"}, "/"); !errors.Is(err, ErrOff) {
				t.Fatalf("Command on an off sandbox: %v, want ErrOff", err)
			}
		})
	}
}

func TestNewIsOffWhenTheSelfTestCannotRun(t *testing.T) {
	s := New("/nonexistent", Policy{}, fakeKernel{euid: 1000, abi: 5}, time.Unix(0, 0))
	if ok, r := s.Ready(); ok || r != SelfTestFailed {
		t.Fatalf("Ready() = %v, %v; want false, SelfTestFailed", ok, r)
	}
}

func TestMergeEnvReplacesAddsAndDrops(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "HOME=/home/n", "LANG=C.UTF-8"}
	got := mergeEnv(base, []string{"HOME=/w", "FOO=1", "NOEQUALS"})
	want := []string{"PATH=/usr/bin:/bin", "HOME=/w", "LANG=C.UTF-8", "FOO=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeEnv = %q, want %q", got, want)
	}
	if base[1] != "HOME=/home/n" {
		t.Fatalf("mergeEnv changed the policy's slice: %q", base)
	}
}
