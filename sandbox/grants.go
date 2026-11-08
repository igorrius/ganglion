package sandbox

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Grant is a path a command may reach, with everything beneath it when Dir.
type Grant struct {
	Path string
	Dir  bool
}

// Lister lists a folder's entries without following symlinks (os.ReadDir).
type Lister func(dir string) ([]fs.DirEntry, error)

// Grants is what Landlock grants so that a command reaches everything under
// "/" but deny: Landlock only grants whole trees, so each denied path's
// ancestors are split into their other entries. A symlink among them is
// never granted, since granting it would grant its target; what it points
// to is reached, or not, by its own path. Something created later directly
// in a split folder is denied: the grants were listed before it existed.
// deny holds clean absolute paths with no symlink in them (DenyList).
func Grants(deny []string, list Lister) ([]Grant, error) {
	return split("/", deny, list)
}

func split(dir string, deny []string, list Lister) ([]Grant, error) {
	if slices.Contains(deny, dir) {
		return nil, nil
	}
	if !slices.ContainsFunc(deny, func(d string) bool { return under(d, dir) }) {
		return []Grant{{Path: dir, Dir: true}}, nil
	}
	entries, err := list(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: list %s: %w", dir, err)
	}
	var out []Grant
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
		case e.IsDir():
			gs, err := split(p, deny, list)
			if err != nil {
				return nil, err
			}
			out = append(out, gs...)
		case !slices.Contains(deny, p):
			out = append(out, Grant{Path: p})
		}
	}
	return out, nil
}

// under reports whether p lies strictly beneath dir.
func under(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}
