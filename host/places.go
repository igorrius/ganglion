// Package host is the node's machine: its own places on disk and their
// cleanup (R-CMD-8), the numbers host.stats reports (R-CMD-5), and the
// runner of host commands in the sandbox (R-CMD-1, R-CMD-4).
package host

import (
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
	PlaceLanguages PlaceName = "languages"
	PlaceClaude    PlaceName = "claude work folder"
	PlaceTemp      PlaceName = "leftovers in the temp folder"
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
