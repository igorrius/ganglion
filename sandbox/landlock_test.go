//go:build linux

package sandbox

import "testing"

func TestRightsByABI(t *testing.T) {
	for abi, want := range map[int]uint64{1: 0x1FFF, 2: 0x3FFF, 3: 0x7FFF, 4: 0x7FFF, 5: 0xFFFF, 7: 0xFFFF} {
		if got := rights(abi); got != want {
			t.Errorf("rights(%d) = %#x, want %#x", abi, got, want)
		}
	}
}
