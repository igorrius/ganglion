package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path string, size int, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureSumsAFolderTree(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	touch(t, filepath.Join(dir, "a"), 100, now)
	touch(t, filepath.Join(dir, "sub", "b"), 50, now.Add(-2*time.Hour))
	u := Measure(Place{Name: PlaceLogs, Path: dir})
	if u.Bytes != 150 || u.Entries != 2 || u.Partial {
		t.Fatalf("usage = %+v, want 150 bytes in 2 files", u)
	}
	if d := u.Oldest.Sub(now.Add(-2 * time.Hour)); d < -time.Second || d > time.Second {
		t.Fatalf("oldest = %v, want two hours ago", u.Oldest)
	}
}

func TestMeasureWithAPrefixCountsOnlyItsEntries(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	touch(t, filepath.Join(dir, "ganglion.db"), 10, now)
	touch(t, filepath.Join(dir, "ganglion.db-wal"), 5, now)
	touch(t, filepath.Join(dir, "other.db"), 1000, now)
	u := Measure(Place{Name: PlaceDatabase, Path: dir, Prefix: "ganglion.db"})
	if u.Bytes != 15 || u.Entries != 2 {
		t.Fatalf("usage = %+v, want the database and its journal only", u)
	}
}

func TestMeasureDoesNotFollowSymlinks(t *testing.T) {
	dir, outside, now := t.TempDir(), t.TempDir(), time.Now()
	touch(t, filepath.Join(outside, "big"), 1<<20, now)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if u := Measure(Place{Name: PlaceClaude, Path: dir}); u.Bytes >= 1<<20 {
		t.Fatalf("a symlink was followed: %+v", u)
	}
}

func TestMeasureOfAMissingPlaceIsZero(t *testing.T) {
	u := Measure(Place{Name: PlaceLogs, Path: filepath.Join(t.TempDir(), "none")})
	if u.Bytes != 0 || u.Entries != 0 || u.Partial {
		t.Fatalf("usage = %+v", u)
	}
}
