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
// lower halves of its first and second arguments (little-endian).
const (
	offNr   = 0
	offArch = 4
	offArg0 = 16
	offArg1 = 24
)

// The file-attribute ioctls a confined command may not make (ADR-0064):
// chattr's flags and the extended file attributes. They are _IOW('f', 2,
// long), _IOW('f', 2, int) and _IOW('X', 32, struct fsxattr), the same on
// amd64, arm64 and riscv64; x/sys names only the first.
const (
	fsIOCSetFlags   = unix.FS_IOC_SETFLAGS
	fsIOC32SetFlags = 0x40046602
	fsIOCFSSetXattr = 0x401c5820
)

// grantedModeBits are the mode bits fchmod may not set in a confined
// command: group and other write, setuid, setgid, sticky. Taking bits away
// stays possible: cargo sets 0644 on the files it unpacks.
const grantedModeBits = 0o7022

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
	var checks []argCheck
	if confine {
		refused = append(refused, metadataCalls()...)
		checks = confinedChecks
	}
	prog := filter(arch, refused, checks)
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if _, _, errno := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&fprog))); errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	runtime.KeepAlive(prog)
	return nil
}

// metadataCalls are the calls that change a file's mode, owner or extended
// attributes: Landlock (to ABI 7 at least) governs none of them, so a
// confined command could otherwise chmod a file it may only read, or a
// denied folder it may list (ADR-0064). fchmod is checked by its mode
// instead (confinedChecks); file times stay: cargo sets them.
func metadataCalls() []uint32 {
	return append([]uint32{
		unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2, unix.SYS_FCHOWNAT, unix.SYS_FCHOWN,
		unix.SYS_SETXATTR, unix.SYS_LSETXATTR, unix.SYS_FSETXATTR,
		unix.SYS_REMOVEXATTR, unix.SYS_LREMOVEXATTR, unix.SYS_FREMOVEXATTR,
		unix.SYS_SETXATTRAT, unix.SYS_REMOVEXATTRAT,
	}, legacyMetadataCalls...)
}

// argCheck refuses call nr when the lower half of its second argument has
// a bit of mask, or, for a check with no mask, equals one of values.
type argCheck struct {
	nr     uint32
	mask   uint32
	values []uint32
}

// confinedChecks are the calls a confined command makes only with some
// arguments: fchmod that grants no bit of grantedModeBits, and ioctl but
// the file-attribute ones.
var confinedChecks = []argCheck{
	{nr: unix.SYS_FCHMOD, mask: grantedModeBits},
	{nr: unix.SYS_IOCTL, values: []uint32{fsIOCSetFlags, fsIOC32SetFlags, fsIOCFSSetXattr}},
}

// filter is the program: another architecture and x32 refused, each call
// of refused refused, each check of checks applied, socket(AF_UNIX, …)
// refused, the rest allowed.
func filter(arch uint32, refused []uint32, checks []argCheck) []unix.SockFilter {
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
	for _, c := range checks {
		prog = append(prog, c.program(deny)...)
	}
	return append(prog,
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.SYS_SOCKET, 0, 3),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, offArg0),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 0, 1),
		stmt(unix.BPF_RET|unix.BPF_K, deny),
		stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
	)
}

// program is c as a block that ends in a verdict for its call and is
// skipped whole for any other: the accumulator holds the call's number on
// the way in and again after it.
func (c argCheck) program(deny uint32) []unix.SockFilter {
	var test []unix.SockFilter
	if c.mask != 0 {
		test = append(test, jump(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, c.mask, 0, 1))
	}
	for i, v := range c.values {
		left := uint8(len(c.values) - i - 1) // tests after this one: a match jumps over them to deny
		if i == len(c.values)-1 {
			test = append(test, jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, v, 0, 1))
			continue
		}
		test = append(test, jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, v, left, 0))
	}
	block := append([]unix.SockFilter{stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, offArg1)}, test...)
	block = append(block, stmt(unix.BPF_RET|unix.BPF_K, deny), stmt(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW))
	return append([]unix.SockFilter{jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, c.nr, 0, uint8(len(block)))}, block...)
}

func stmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}
