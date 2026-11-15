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

func TestCheckPlacesRejectsAnAgedPlaceThatWouldEmptyASharedFolder(t *testing.T) {
	data := t.TempDir()
	home, _ := os.UserHomeDir()
	db := Place{Name: PlaceDatabase, Path: data, Prefix: "ganglion.db"}
	cases := map[string]Place{
		"relative":          {Name: PlaceClaude, Path: "claude", MaxAge: time.Hour},
		"empty path":        {Name: PlaceClaude, Path: "", MaxAge: time.Hour},
		"root":              {Name: PlaceClaude, Path: string(filepath.Separator), MaxAge: time.Hour},
		"temp folder":       {Name: PlaceTemp, Path: os.TempDir(), MaxAge: time.Hour},
		"home":              {Name: PlaceClaude, Path: home, MaxAge: time.Hour},
		"another's folder":  {Name: PlaceClaude, Path: data, MaxAge: time.Hour},
		"above another's":   {Name: PlaceClaude, Path: filepath.Dir(data), MaxAge: time.Hour},
		"unclean, the same": {Name: PlaceClaude, Path: data + "/sub/..", MaxAge: time.Hour},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := CheckPlaces([]Place{db, p}); err == nil {
				t.Fatalf("CheckPlaces accepted %+v", p)
			}
		})
	}
}

func TestCheckPlacesAcceptsTheNodesOwnAgedFolders(t *testing.T) {
	data := t.TempDir()
	ps := []Place{
		{Name: PlaceDatabase, Path: data, Prefix: "ganglion.db"},
		{Name: PlaceModules, Path: filepath.Join(data, "modules")},
		{Name: PlaceReleases, Path: filepath.Join(data, "releases")},
		{Name: PlaceClaude, Path: filepath.Join(data, "claude"), MaxAge: time.Hour},
		{Name: PlaceBuilds, Path: filepath.Join(data, "modules", "builds"), MaxAge: time.Hour},
		{Name: PlaceTemp, Path: os.TempDir(), Prefix: "ganglion-claude-", MaxAge: time.Hour},
		{Name: PlaceLogs, Path: string(filepath.Separator)}, // not aged: never emptied
	}
	if err := CheckPlaces(ps); err != nil {
		t.Fatal(err)
	}
}
