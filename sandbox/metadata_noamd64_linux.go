//go:build linux && !amd64

package sandbox

// legacyMetadataCalls is empty where the architecture has only the *at
// forms (arm64, riscv64).
var legacyMetadataCalls []uint32
