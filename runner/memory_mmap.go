//go:build linux || darwin

package runner

import (
	"syscall"

	"github.com/tetratelabs/wazero/experimental"
)

// allocator backs each instance's linear memory with one anonymous
// mapping of its maximum size (the manifest's limit): growing never
// copies the memory, pages count towards RSS only once the module
// touches them, and closing the instance returns them to the system at
// once. wazero's default grows a Go slice by copying, leaving the old
// one for the garbage collector, which roughly doubled a call's peak
// (ADR-0035, ruling R20).
var allocator experimental.MemoryAllocator = experimental.MemoryAllocatorFunc(mmapMemory)

func mmapMemory(_, maxBytes uint64) experimental.LinearMemory {
	b, err := syscall.Mmap(-1, 0, int(maxBytes), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return noMemory{}
	}
	return &mapped{buf: b}
}

// mapped is one mapping; Reallocate hands out a prefix of it.
type mapped struct{ buf []byte }

func (m *mapped) Reallocate(size uint64) []byte {
	if m.buf == nil || size > uint64(len(m.buf)) {
		return nil
	}
	return m.buf[:size]
}

func (m *mapped) Free() {
	if m.buf != nil {
		_ = syscall.Munmap(m.buf)
		m.buf = nil
	}
}

// noMemory is a mapping that could not be made: every allocation fails,
// so the call fails instead of the process.
type noMemory struct{}

func (noMemory) Reallocate(uint64) []byte { return nil }
func (noMemory) Free()                    {}
