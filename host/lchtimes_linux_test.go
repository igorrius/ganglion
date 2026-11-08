package host

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// lchtimes sets a symlink's own times, not its target's.
func lchtimes(t *testing.T, path string, at time.Time) {
	t.Helper()
	ts := []unix.Timespec{unix.NsecToTimespec(at.UnixNano()), unix.NsecToTimespec(at.UnixNano())}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
}
