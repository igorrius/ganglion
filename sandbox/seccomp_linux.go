//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// auditArchs is the seccomp architecture of each GOARCH the filter knows.
var auditArchs = map[string]uint32{
	"amd64": unix.AUDIT_ARCH_X86_64, "arm64": unix.AUDIT_ARCH_AARCH64, "riscv64": unix.AUDIT_ARCH_RISCV64,
}

// x32Bit marks the x32 system calls on amd64, which the filter refuses
// rather than read under the wrong numbers.
const x32Bit = 0x40000000

// seccompData's offsets: the call's number, its architecture, and the
// lower half of its first argument (little-endian).
const (
	offNr   = 0
	offArch = 4
	offArg0 = 16
)

// errNoArch is a GOARCH the socket filter has no architecture for.
var errNoArch = errors.New("no seccomp architecture for " + runtime.GOARCH)

// refuseUnixSockets installs a seccomp filter on the calling thread that
// makes socket(AF_UNIX, …) and io_uring_setup fail with EPERM, and refuses
// every call made under another architecture. Landlock below ABI 9 cannot
// tell which socket files a command may connect to, and through them it
// would reach gpg-agent or the keyring; the owner chose no Unix sockets at
// all there (2026-11-08, ADR-0039). io_uring is refused because it can open
// sockets past the filter. socketpair stays: it reaches no one. For a
// confined command it also refuses changing a file's mode, owner or
// extended attributes by path (metadataCalls), which Landlock does not
// govern (ADR-0064).
func refuseUnixSockets(confine bool) error {
	arch, ok := auditArchs[runtime.GOARCH]
	if !ok {
		return errNoArch
	}
	refused := []uint32{unix.SYS_IO_URING_SETUP}
	if confine {
		refused = append(refused, metadataCalls()...)
	}
	prog := filter(arch, refused)
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if _, _, errno := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&fprog))); errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	runtime.KeepAlive(prog)
	return nil
}

// metadataCalls are the calls that change a file's mode, owner or extended
// attributes by its path: Landlock (to ABI 7 at least) governs none of
// them, so a confined command could otherwise chmod a file it may only read
// (ADR-0064). The fd-based ones (fchmod, fchown, fsetxattr) and utimensat
// stay: cargo sets times, and copies a file's mode through its fd.
func metadataCalls() []uint32 {
	return append([]uint32{
		unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2, unix.SYS_FCHOWNAT,
		unix.SYS_SETXATTR, unix.SYS_LSETXATTR, unix.SYS_REMOVEXATTR, unix.SYS_LREMOVEXATTR,
		unix.SYS_SETXATTRAT, unix.SYS_REMOVEXATTRAT,
	}, legacyMetadataCalls...)
}

// filter is the program: another architecture and x32 refused, each call
// of refused refused, socket(AF_UNIX, …) refused, the rest allowed.
func filter(arch uint32, refused []uint32) []unix.SockFilter {
	deny := unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	prog := []unix.SockFilter{
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, offArch),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, arch, 1, 0),
		stmt(unix.BPF_RET|unix.BPF_K, deny), // another architecture
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, offNr),
		jump(unix.BPF_JMP|unix.BPF_JGE|unix.BPF_K, x32Bit, 0, 1),
		stmt(unix.BPF_RET|unix.BPF_K, deny), // x32
	}
	for _, nr := range refused {
		prog = append(prog,
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, nr, 0, 1),
			stmt(unix.BPF_RET|unix.BPF_K, deny))
	}
	return append(prog,
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.SYS_SOCKET, 0, 3),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, offArg0),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 0, 1),
		stmt(unix.BPF_RET|unix.BPF_K, deny),
		stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
	)
}

func stmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}
