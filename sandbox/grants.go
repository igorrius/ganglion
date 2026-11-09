package sandbox

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Grant is a path a command may reach, with everything beneath it when Dir.
// A List grant is a folder split around a denied path: it and every folder
// beneath may be listed, and nothing else (Landlock's READ_DIR, ADR-0057).
type Grant struct {
	Path string
	Dir  bool
	List bool
}

// Lister lists a folder's entries without following symlinks (os.ReadDir).
type Lister func(dir string) ([]fs.DirEntry, error)

// Grants is what Landlock grants so that a command reaches everything under
// "/" but deny: Landlock only grants whole trees, so each denied path's
// ancestors are split into their other entries. A symlink among them is
// never granted, since granting it would grant its target; what it points
// to is reached, or not, by its own path. Something created later directly
// in a split folder is denied, its names aside: the grants were listed
// before it existed. Unless hideNames, each split folder also gets a List
// grant, so the names inside denied folders can be listed but not their
// contents (ADR-0057); a denied folder itself is never granted. deny holds clean absolute paths
// with no symlink in them (DenyList).
func Grants(deny []string, list Lister, hideNames bool) ([]Grant, error) {
	return splitter{deny: deny, list: list, hideNames: hideNames}.split("/")
}

// splitter is what Grants splits by.
type splitter struct {
	deny      []string
	list      Lister
	hideNames bool
}

func (s splitter) split(dir string) ([]Grant, error) {
	if slices.Contains(s.deny, dir) {
		return nil, nil
	}
	if !slices.ContainsFunc(s.deny, func(d string) bool { return under(d, dir) }) {
		return []Grant{{Path: dir, Dir: true}}, nil
	}
	entries, err := s.list(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: list %s: %w", dir, err)
	}
	var out []Grant
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
		case e.IsDir():
			gs, err := s.split(p)
			if err != nil {
				return nil, err
			}
			out = append(out, gs...)
		case !slices.Contains(s.deny, p):
			out = append(out, Grant{Path: p})
		}
	}
	if !s.hideNames {
		out = append(out, Grant{Path: dir, Dir: true, List: true})
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
