//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fileRights are the rights Landlock accepts on a file rather than a folder.
const fileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

// landlockABI is the kernel's Landlock version; an error when it has none.
func landlockABI() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return int(v), nil
}

// rights is every file system right ABI abi knows, up to version 5. Unix
// socket connections (ABI 9) are not handled here: until that version is
// built on, the helper refuses Unix sockets with seccomp (refuseUnixSockets).
func rights(abi int) uint64 {
	r := uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM<<1 - 1) // version 1: EXECUTE … MAKE_SYM
	if abi >= 2 {
		r |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		r |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		r |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return r
}

// rulesetAttr is struct landlock_ruleset_attr up to its scopes (ABI 6); a
// kernel that knows less accepts it while the fields it does not know are 0.
type rulesetAttr struct{ handledAccessFS, handledAccessNet, scoped uint64 }

// scopes is what the domain keeps to itself from ABI 6 on: abstract Unix
// sockets, so a command cannot reach a listener outside the sandbox by name.
func scopes(abi int) uint64 {
	if abi < 6 {
		return 0
	}
	return unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
}

// restrict confines the calling thread, and what it execs, to grants.
func restrict(abi int, grants []Grant) error {
	handled := rights(abi)
	attr := rulesetAttr{handledAccessFS: handled, scoped: scopes(abi)}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(fd)) }()
	for _, g := range grants {
		if err := addRule(int(fd), g, handled); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

func addRule(ruleset int, g Grant, handled uint64) error {
	p, err := unix.Open(g.Path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil // gone since it was listed: nothing to grant
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", g.Path, err)
	}
	defer func() { _ = unix.Close(p) }()
	access := handled
	if !g.Dir {
		access &= fileRights
	}
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(p)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", g.Path, errno)
	}
	return nil
}

// specFile is sp as JSON in an anonymous memory file, read from its start.
func specFile(sp spec) (*os.File, error) {
	fd, err := unix.MemfdCreate("ganglion-sandbox", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("sandbox: memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "sandbox-spec")
	if err := json.NewEncoder(f).Encode(sp); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sandbox: spec: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sandbox: spec: %w", err)
	}
	return f, nil
}
