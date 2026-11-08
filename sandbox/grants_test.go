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

func paths(gs []Grant) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Path)
	}
	slices.Sort(out)
	return out
}

func TestGrantsWithoutDenyIsTheRoot(t *testing.T) {
	got, err := Grants(nil, tree(t, fstest.MapFS{"etc/hosts": {}}))
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
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m))
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
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(paths(got), "/home/u/keys") {
		t.Fatal("a symlink next to a denied path was granted; it would grant its target")
	}
}

func TestGrantsDenyAFile(t *testing.T) {
	m := fstest.MapFS{"srv/app/ganglion.db": {}, "srv/app/ganglion": {}}
	got, err := Grants([]string{"/srv/app/ganglion.db"}, tree(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/srv/app/ganglion"}; !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestGrantsFailOnAnUnlistableAncestor(t *testing.T) {
	list := func(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
	if _, err := Grants([]string{"/home/u/.ssh"}, list); err == nil {
		t.Fatal("an ancestor that cannot be listed must fail the command, not grant it whole")
	}
}
