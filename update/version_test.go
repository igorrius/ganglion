package update

import (
	"errors"
	"testing"
)

func TestParseVersionRefusals(t *testing.T) {
	for _, s := range []string{"", "1.2.3", "v1.2", "v01.2.3", "v1.02.3", "v1.2.3-", "v1.2.3-01", "v1.2.3+build", "v1.2.3-a..b",
		"../x", "v1.2.3/../x", "dev", "v1.2.3 ", "v99999999999999999999.0.0", "v1.2.3-" + string(make([]byte, 80))} {
		if _, err := ParseVersion(s); !errors.Is(err, ErrBadVersion) {
			t.Errorf("%q: %v, want ErrBadVersion", s, err)
		}
	}
	for _, s := range []string{"v0.0.0", "v1.2.3", "v1.10.0", "v1.2.3-rc.1", "v1.2.3-0", "v1.2.3-x-y"} {
		v, err := ParseVersion(s)
		if err != nil || v.String() != s {
			t.Errorf("%q: %v %v", s, v, err)
		}
	}
}

func TestCompare(t *testing.T) {
	// The ordering of semver 2.0's own example, then the plain cases.
	order := []string{"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta", "v1.0.0-beta.2",
		"v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.2.3", "v1.10.0", "v2.0.0"}
	for i, a := range order {
		for j, b := range order {
			va, _ := ParseVersion(a)
			vb, _ := ParseVersion(b)
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(va, vb); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestRunning(t *testing.T) {
	for _, s := range []string{"dev", "", "v0.2.1-3-gabc1234", "v0.2.1-dirty", "v0.2.1-3-gabc1234-dirty", "0.2.1", "(devel)"} {
		if _, err := Running(s); !errors.Is(err, ErrDevBuild) {
			t.Errorf("%q: %v, want ErrDevBuild", s, err)
		}
	}
	for _, s := range []string{"v0.2.1", "v0.3.0-rc.1"} {
		if v, err := Running(s); err != nil || v.String() != s {
			t.Errorf("%q: %v %v", s, v, err)
		}
	}
}
