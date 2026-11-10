//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

// legacyMetadataCalls are the path-based mode and owner calls amd64 keeps
// beside their *at forms (ADR-0064).
var legacyMetadataCalls = []uint32{unix.SYS_CHMOD, unix.SYS_CHOWN, unix.SYS_LCHOWN}
