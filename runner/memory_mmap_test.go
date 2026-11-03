//go:build linux || darwin

package runner

import (
	"testing"
	"unsafe"
)

func TestMappedMemoryGrowsInPlace(t *testing.T) {
	m := mmapMemory(0, 4<<20)
	a := m.Reallocate(64 << 10)
	a[0] = 7
	b := m.Reallocate(1 << 20)
	if len(b) != 1<<20 || unsafe.SliceData(a) != unsafe.SliceData(b) || b[0] != 7 {
		t.Fatal("growing moved or lost the memory")
	}
	if m.Reallocate(4<<20+1) != nil {
		t.Fatal("grew past the maximum")
	}
	m.Free()
	if m.Reallocate(1) != nil {
		t.Fatal("a freed memory grew")
	}
	m.Free() // twice is harmless
}
