//go:build linux

package sandbox

import (
	"errors"
	"testing"
	"time"
)

type fakeKernel struct {
	euid     int
	abi      int
	abiErr   error
	dumpable bool
}

func (k fakeKernel) Euid() int                 { return k.euid }
func (k fakeKernel) LandlockABI() (int, error) { return k.abi, k.abiErr }
func (k fakeKernel) Dumpable() (bool, error)   { return k.dumpable, nil }

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
