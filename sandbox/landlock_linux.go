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

// readRights are what a confined command may do where it may not write
// (ADR-0064): run, read and list. Device ioctls go with writing: a build
// needs none outside its folder.
const readRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_DIR

// scopes is what the domain keeps to itself from ABI 6 on: abstract Unix
// sockets, so a command cannot reach a listener outside the sandbox by name,
// and for a confined command signals too, so it cannot kill the helper that
// reaps what it leaves (ADR-0064). host.exec keeps signals open: kill must
// reach the owner's processes (ADR-0039).
func scopes(abi int, confine bool) uint64 {
	if abi < 6 {
		return 0
	}
	if confine {
		return unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	return unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
}

// restrict confines the calling thread, and what it execs, to grants; a
// confined command writes only through its Write grants.
func restrict(abi int, grants []Grant, confine bool) error {
	handled := rights(abi)
	attr := rulesetAttr{handledAccessFS: handled, scoped: scopes(abi, confine)}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(fd)) }()
	for _, g := range grants {
		if err := addRule(int(fd), g, handled, confine); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

func addRule(ruleset int, g Grant, handled uint64, confine bool) error {
	p, err := unix.Open(g.Path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil // gone since it was listed: nothing to grant
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", g.Path, err)
	}
	defer func() { _ = unix.Close(p) }()
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access(g, handled, confine), Parent_fd: int32(p)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", g.Path, errno)
	}
	return nil
}

// access is what g grants of handled: listing alone for a List grant
// (ADR-0057), a file's rights for a file, all of them for a folder; for a
// confined command, reading alone where g is not a Write grant (ADR-0064).
func access(g Grant, handled uint64, confine bool) uint64 {
	r := handled
	switch {
	case g.List:
		return handled & unix.LANDLOCK_ACCESS_FS_READ_DIR
	case confine && !g.Write:
		r &= readRights
	}
	if !g.Dir {
		r &= fileRights
	}
	return r
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
