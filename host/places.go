// Package host is the node's machine: its own places on disk and their
// cleanup (R-CMD-8), the numbers host.stats reports (R-CMD-5), and the
// runner of host commands in the sandbox (R-CMD-1, R-CMD-4).
package host

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PlaceName is what a place holds, as host.stats names it.
type PlaceName string

// The node's places.
const (
	PlaceDatabase  PlaceName = "database"
	PlaceLogs      PlaceName = "logs"
	PlaceWorkspace PlaceName = "workspace"
	PlaceModules   PlaceName = "modules"
	PlaceReleases  PlaceName = "releases"
	PlaceLanguages PlaceName = "languages"
	PlaceClaude    PlaceName = "claude work folder"
	PlaceTemp      PlaceName = "leftovers in the temp folder"
	PlaceBuilds    PlaceName = "module builds"
)

// maxEntries caps the files one measure walks, so a huge tree cannot hold
// host.stats up; past it the usage is Partial.
const maxEntries = 200_000

// Place is something the node writes on disk: a folder, or the entries of
// a folder whose names start with Prefix. A MaxAge above zero is how long an
// entry may stay before the janitor removes it.
type Place struct {
	Name   PlaceName
	Path   string
	Prefix string
	MaxAge time.Duration
}

// Usage is how much a place holds.
type Usage struct {
	Place   Place
	Bytes   int64
	Entries int       // regular files
	Oldest  time.Time // the oldest file's change time; zero when there is none
	Partial bool      // the walk stopped at maxEntries
}

// Measure walks the place without following symlinks. A place that is
// missing or cannot be read holds nothing.
func Measure(p Place) Usage {
	u := Usage{Place: p}
	for _, top := range entries(p) {
		_ = filepath.WalkDir(top, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: skipped, not fatal
			}
			if u.Entries >= maxEntries {
				u.Partial = true
				return filepath.SkipAll
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			u.Bytes, u.Entries = u.Bytes+info.Size(), u.Entries+1
			if t := info.ModTime(); u.Oldest.IsZero() || t.Before(u.Oldest) {
				u.Oldest = t
			}
			return nil
		})
	}
	return u
}

// entries are the paths a place covers: the folder itself, or its
// entries that start with Prefix.
func entries(p Place) []string {
	if p.Prefix == "" {
		return []string{p.Path}
	}
	return children(p)
}

// children are the entries of the place's folder whose names start with
// Prefix; with no Prefix, all of them. The janitor ages these, never the
// folder itself, which the node keeps using.
func children(p Place) []string {
	list, err := os.ReadDir(p.Path)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range list {
		if strings.HasPrefix(e.Name(), p.Prefix) {
			out = append(out, filepath.Join(p.Path, e.Name()))
		}
	}
	return out
}

// CheckPlaces rejects an aged place without a Prefix that is not a folder of
// the node's own: the janitor would empty all of it. Such a place must be
// an absolute path that is not the root, the temp folder or the user's
// home, and that holds no other place.
func CheckPlaces(ps []Place) error {
	home, _ := os.UserHomeDir()
	shared := map[string]bool{filepath.Clean(os.TempDir()): true}
	if home != "" {
		shared[filepath.Clean(home)] = true
	}
	for i, p := range ps {
		if p.MaxAge <= 0 || p.Prefix != "" {
			continue
		}
		dir := filepath.Clean(p.Path)
		switch {
		case p.Path == "" || !filepath.IsAbs(dir):
			return fmt.Errorf("host: the %s place %q is aged without a prefix and is not an absolute path", p.Name, p.Path)
		case dir == filepath.Dir(dir) || shared[dir]:
			return fmt.Errorf("host: the %s place %q is aged without a prefix and is a shared folder", p.Name, p.Path)
		}
		for j, q := range ps {
			if j != i && within(dir, filepath.Clean(q.Path)) {
				return fmt.Errorf("host: the %s place %q is aged without a prefix and holds the %s place", p.Name, p.Path, q.Name)
			}
		}
	}
	return nil
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
