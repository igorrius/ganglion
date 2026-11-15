package update

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Errors of a version.
var (
	// ErrBadVersion is a string that is not a core version.
	ErrBadVersion = errors.New("update: not a core version")
	// ErrDevBuild is a running build that reports no release version; it
	// cannot be moved by /update (ADR-0081).
	ErrDevBuild = errors.New("update: this build has no release version")
)

// Version is a core version, vMAJOR.MINOR.PATCH with an optional
// -<pre-release>, ordered as semver 2.0. Build metadata is not accepted.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // the dot-separated pre-release identifiers
	raw                 string
}

// String is the version as written, vX.Y.Z[-pre].
func (v Version) String() string { return v.raw }

// IsZero says whether v is the zero Version, no version at all.
func (v Version) IsZero() bool { return v.raw == "" }

var (
	coreVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	numeric     = regexp.MustCompile(`^[0-9]+$`)
	// describeTail is what `git describe` adds to a tag when the build is
	// not exactly the tag, or from a dirty tree.
	describeTail = regexp.MustCompile(`(-[0-9]+-g[0-9a-f]+(-dirty)?|-dirty)$`)
	// maxVersionLen keeps a hostile string from becoming a long path or a
	// long number.
	maxVersionLen = 64
)

// ParseVersion reads a core version strictly: a leading v, three numbers
// without leading zeros, and a pre-release whose numeric identifiers have
// none either. The result is safe to use as a folder name.
func ParseVersion(s string) (Version, error) {
	m := coreVersion.FindStringSubmatch(s)
	if m == nil || len(s) > maxVersionLen {
		return Version{}, fmt.Errorf("%w: %q", ErrBadVersion, truncate(s))
	}
	v := Version{raw: s}
	for i, dst := range []*int{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("%w: %q", ErrBadVersion, truncate(s))
		}
		*dst = n
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
		for _, id := range v.Pre {
			if numeric.MatchString(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, fmt.Errorf("%w: %q", ErrBadVersion, truncate(s))
			}
		}
	}
	return v, nil
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}

// Running reads the version a running build reports (buildinfo's
// Version). Only an exact core version qualifies: "dev", a `git describe`
// like v0.2.1-3-gabc and a dirty build are ErrDevBuild.
func Running(v string) (Version, error) {
	ver, err := ParseVersion(v)
	if err != nil || describeTail.MatchString(v) {
		return Version{}, fmt.Errorf("%w: %q", ErrDevBuild, truncate(v))
	}
	return ver, nil
}

// Compare orders two versions: -1, 0 or 1. A pre-release is below its
// release; pre-release identifiers compare one by one, numeric ones as
// numbers and below alphanumeric ones, a shorter list below a longer one
// it prefixes.
func Compare(a, b Version) int {
	for _, p := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := cmpInt(p[0], p[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := cmpIdent(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Pre), len(b.Pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpIdent(a, b string) int {
	an, bn := numeric.MatchString(a), numeric.MatchString(b)
	switch {
	case an && bn:
		// Equal length numbers compare as strings; longer is larger (no
		// leading zeros), so no overflow on a hostile identifier.
		if c := cmpInt(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
