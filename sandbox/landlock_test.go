//go:build linux

package sandbox

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestRightsByABI(t *testing.T) {
	for abi, want := range map[int]uint64{1: 0x1FFF, 2: 0x3FFF, 3: 0x7FFF, 4: 0x7FFF, 5: 0xFFFF, 7: 0xFFFF} {
		if got := rights(abi); got != want {
			t.Errorf("rights(%d) = %#x, want %#x", abi, got, want)
		}
	}
}

// ADR-0057: a List grant is READ_DIR and nothing else, at every ABI, and the
// other grants keep their rights.
func TestAccessOfEachGrant(t *testing.T) {
	for _, abi := range []int{1, 3, 5, 7} {
		h := rights(abi)
		if got := access(Grant{Path: "/home", Dir: true, List: true}, h, false); got != unix.LANDLOCK_ACCESS_FS_READ_DIR {
			t.Errorf("ABI %d: a list grant gives %#x, want READ_DIR only", abi, got)
		}
		if got := access(Grant{Path: "/etc", Dir: true}, h, false); got != h {
			t.Errorf("ABI %d: a folder grant gives %#x, want %#x", abi, got, h)
		}
		if got := access(Grant{Path: "/etc/hosts"}, h, false); got != h&fileRights {
			t.Errorf("ABI %d: a file grant gives %#x, want %#x", abi, got, h&fileRights)
		}
	}
}

// ADR-0064: a confined command reads, runs and lists where it has no Write
// grant, and keeps every right beneath one; a List grant stays READ_DIR.
func TestAccessOfAConfinedCommand(t *testing.T) {
	for _, abi := range []int{3, 5, 7} {
		h := rights(abi)
		if got := access(Grant{Path: "/usr", Dir: true}, h, true); got != readRights {
			t.Errorf("ABI %d: a read folder gives %#x, want %#x", abi, got, uint64(readRights))
		}
		if got := access(Grant{Path: "/etc/hosts"}, h, true); got != readRights&fileRights {
			t.Errorf("ABI %d: a read file gives %#x, want %#x", abi, got, uint64(readRights&fileRights))
		}
		if got := access(Grant{Path: "/tmp/b", Dir: true, Write: true}, h, true); got != h {
			t.Errorf("ABI %d: a write folder gives %#x, want %#x", abi, got, h)
		}
		if got := access(Grant{Path: "/dev/null", Write: true}, h, true); got != h&fileRights {
			t.Errorf("ABI %d: a write file gives %#x, want %#x", abi, got, h&fileRights)
		}
		if got := access(Grant{Path: "/tmp", Dir: true, List: true}, h, true); got != unix.LANDLOCK_ACCESS_FS_READ_DIR {
			t.Errorf("ABI %d: a list grant gives %#x", abi, got)
		}
	}
}

func TestSignalsAreScopedForAConfinedCommandOnly(t *testing.T) {
	if scopes(7, false)&unix.LANDLOCK_SCOPE_SIGNAL != 0 {
		t.Error("host.exec's signals are scoped: kill must reach the owner's processes (ADR-0039)")
	}
	if scopes(7, true)&unix.LANDLOCK_SCOPE_SIGNAL == 0 {
		t.Error("a confined command's signals are not scoped (ADR-0064)")
	}
	if scopes(5, true) != 0 {
		t.Error("scopes asked of a kernel below ABI 6")
	}
}
