//go:build !(linux || darwin)

package runner

import "github.com/tetratelabs/wazero/experimental"

// allocator is wazero's default elsewhere.
var allocator experimental.MemoryAllocator
