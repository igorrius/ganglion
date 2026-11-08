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
		if got := access(Grant{Path: "/home", Dir: true, List: true}, h); got != unix.LANDLOCK_ACCESS_FS_READ_DIR {
			t.Errorf("ABI %d: a list grant gives %#x, want READ_DIR only", abi, got)
		}
		if got := access(Grant{Path: "/etc", Dir: true}, h); got != h {
			t.Errorf("ABI %d: a folder grant gives %#x, want %#x", abi, got, h)
		}
		if got := access(Grant{Path: "/etc/hosts"}, h); got != h&fileRights {
			t.Errorf("ABI %d: a file grant gives %#x, want %#x", abi, got, h&fileRights)
		}
	}
}
