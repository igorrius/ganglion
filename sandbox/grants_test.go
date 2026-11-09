package sandbox

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// tree is a listing over an in-memory file system rooted at "/".
func tree(t *testing.T, m fstest.MapFS) Lister {
	t.Helper()
	return func(dir string) ([]fs.DirEntry, error) {
		name := strings.TrimPrefix(dir, "/")
		if name == "" {
			name = "."
		}
		return fs.ReadDir(m, name)
	}
}

// hidden is Grants' hideNames for the tests of the grants that reach
// contents: with it no list grant is added, as before ADR-0057.
const hidden = true

func paths(gs []Grant) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Path)
	}
	slices.Sort(out)
	return out
}

func TestGrantsWithoutDenyIsTheRoot(t *testing.T) {
	got, err := Grants(nil, tree(t, fstest.MapFS{"etc/hosts": {}}), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Grant{{Path: "/", Dir: true}}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGrantsSplitAroundADeniedFolder(t *testing.T) {
	m := fstest.MapFS{
		"etc/hosts": {}, "home/u/.ssh/id": {}, "home/u/code/a.go": {}, "home/u/notes.txt": {},
		"home/v/x": {},
	}
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m), hidden)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/etc", "/home/u/code", "/home/u/notes.txt", "/home/v"}
	if !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
	for _, g := range got {
		if g.Path == "/home/u/notes.txt" && g.Dir {
			t.Fatal("a file granted as a folder")
		}
	}
}

func TestGrantsNeverGrantASymlink(t *testing.T) {
	m := fstest.MapFS{
		"home/u/.ssh/id": {},
		"home/u/keys":    {Mode: fs.ModeSymlink, Data: []byte("/home/u/.ssh")},
		"home/u/a.txt":   {},
	}
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m), hidden)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(paths(got), "/home/u/keys") {
		t.Fatal("a symlink next to a denied path was granted; it would grant its target")
	}
}

func TestGrantsDenyAFile(t *testing.T) {
	m := fstest.MapFS{"srv/app/ganglion.db": {}, "srv/app/ganglion": {}}
	got, err := Grants([]string{"/srv/app/ganglion.db"}, tree(t, m), hidden)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/srv/app/ganglion"}; !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestGrantsFailOnAnUnlistableAncestor(t *testing.T) {
	list := func(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
	if _, err := Grants([]string{"/home/u/.ssh"}, list, false); err == nil {
		t.Fatal("an ancestor that cannot be listed must fail the command, not grant it whole")
	}
}

// home is a machine whose home holds a secret store and other entries.
var home = fstest.MapFS{
	"etc/hosts": {}, "home/o/.ssh/id_ed25519": {}, "home/o/code/a.go": {}, "home/o/notes.txt": {}, "home/v/x": {},
}

// lists is the paths of gs's list grants, sorted; it fails the test on a
// list grant that is not a folder.
func lists(t *testing.T, gs []Grant) []string {
	t.Helper()
	var out []string
	for _, g := range gs {
		if g.List {
			if !g.Dir {
				t.Errorf("a list grant on a file: %+v", g)
			}
			out = append(out, g.Path)
		}
	}
	slices.Sort(out)
	return out
}

// ADR-0057: each folder split around a denied path may be listed, and the
// grants that reach contents stay the ones of hidden names.
func TestGrantsListTheSplitFolders(t *testing.T) {
	got, err := Grants([]string{"/home/o/.ssh"}, tree(t, home), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/", "/home", "/home/o"}; !slices.Equal(lists(t, got), want) {
		t.Fatalf("list grants %v, want %v", lists(t, got), want)
	}
	golden, err := Grants([]string{"/home/o/.ssh"}, tree(t, home), hidden)
	if err != nil {
		t.Fatal(err)
	}
	rest := slices.DeleteFunc(slices.Clone(got), func(g Grant) bool { return g.List })
	slices.SortFunc(rest, func(a, b Grant) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(golden, func(a, b Grant) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(rest, golden) {
		t.Fatalf("the other grants %v, want those of hidden names %v", rest, golden)
	}
}

func TestGrantsWithHiddenNamesListNothing(t *testing.T) {
	got, err := Grants([]string{"/home/o/.ssh"}, tree(t, home), hidden)
	if err != nil {
		t.Fatal(err)
	}
	if l := lists(t, got); len(l) != 0 {
		t.Fatalf("list grants with hidden names: %v", l)
	}
	if want := []string{"/etc", "/home/o/code", "/home/o/notes.txt", "/home/v"}; !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

// A denied folder is never granted, not even its listing: its names are
// reached through its parent's list grant only.
func TestGrantsNeverListADeniedFolder(t *testing.T) {
	m := fstest.MapFS{"home/o/.ssh/keys/id": {}, "home/o/.ssh/config": {}, "home/o/a.txt": {}, "data/ganglion.db": {}}
	deny := []string{"/home/o/.ssh", "/home/o/.ssh/keys", "/data", "/"}
	for _, d := range [][]string{deny[:3], deny} {
		got, err := Grants(d, tree(t, m), false)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range got {
			if slices.Contains(d, g.Path) || slices.ContainsFunc(d, func(x string) bool { return under(g.Path, x) }) {
				t.Errorf("deny %v granted %+v", d, g)
			}
		}
	}
}

// ADR-0064: a write folder is granted whole, as Write grants, when nothing
// beneath it is denied.
func TestWriteGrantsAreTheFolders(t *testing.T) {
	m := fstest.MapFS{"tmp/b/src/lib.rs": {}, "tmp/c/x": {}}
	got, err := WriteGrants([]string{"/tmp/b"}, []string{"/home/u/.ssh"}, tree(t, m), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Grant{{Path: "/tmp/b", Dir: true, Write: true}}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A denied path inside a write folder stays denied: the folder is split
// around it, and its list grants write nothing.
func TestWriteGrantsSplitAroundADeniedPath(t *testing.T) {
	m := fstest.MapFS{"tmp/b/src/.env": {}, "tmp/b/src/lib.rs": {}, "tmp/b/target/x": {}}
	got, err := WriteGrants([]string{"/tmp/b"}, []string{"/tmp/b/src/.env"}, tree(t, m), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/tmp/b", "/tmp/b/src", "/tmp/b/src/lib.rs", "/tmp/b/target"}; !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
	for _, g := range got {
		if g.Write == g.List {
			t.Errorf("%+v: a list grant writes, or a content grant does not", g)
		}
	}
}

func TestWriteGrantsRefuseADeniedOrUncleanFolder(t *testing.T) {
	m := fstest.MapFS{"home/u/.ssh/id": {}, "tmp/b/x": {}}
	for _, write := range [][]string{{"/home/u/.ssh"}, {"/home/u/.ssh/inner"}, {"tmp/b"}, {"/tmp/b/../c"}} {
		if _, err := WriteGrants(write, []string{"/home/u/.ssh"}, tree(t, m), false); err == nil {
			t.Errorf("WriteGrants(%q) granted it", write)
		}
	}
}
