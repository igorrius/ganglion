package host

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

// Setting a symlink's own times is done on Linux, so the test lives here:
// a test file a build tag leaves out on Linux would be a source a build
// reads (internal/testinputs).
func TestSweepRemovesASymlinkNotItsTarget(t *testing.T) {
	dir, target, now := t.TempDir(), t.TempDir(), time.Now()
	touch(t, filepath.Join(target, "precious"), 1, now)
	link := filepath.Join(dir, "run-1")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	lchtimes(t, link, now.Add(-48*time.Hour)) // the link's own time, not its target's
	j := NewJanitor([]Place{{Name: PlaceClaude, Path: dir, MaxAge: time.Hour}}, slog.New(slog.DiscardHandler),
		WithJanitorClock(func() time.Time { return now }, nil))
	j.Sweep(context.Background())
	if _, err := os.Stat(filepath.Join(target, "precious")); err != nil {
		t.Fatal("the janitor followed a symlink")
	}
}
