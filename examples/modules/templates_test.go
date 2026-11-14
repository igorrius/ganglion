package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	templates "github.com/igorrius/ganglion/examples/modules"
)

// onDisk lists the files of the template folders as the repository has them,
// without the local build folders.
func onDisk(t *testing.T) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(p, string(filepath.Separator)) {
			got = append(got, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestEveryFileIsEmbedded(t *testing.T) {
	var embedded []string
	err := fs.WalkDir(templates.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			embedded = append(embedded, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(embedded)
	if disk := onDisk(t); !slices.Equal(embedded, disk) {
		t.Fatalf("embedded\n%v\non disk\n%v\nadd the missing file to the go:embed lines of templates.go", embedded, disk)
	}
}

func TestWriteReplacesTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	stale := filepath.Join(dir, "reader-template", "src", "old.rs")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := templates.Write(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a file of an older version is still there: %v", err)
	}
	for _, name := range []string{"reader-template", "reflex-template"} {
		want, err := os.ReadFile(filepath.Join(name, "manifest.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, name, "manifest.yaml"))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s/manifest.yaml: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name, "src", "lib.rs")); err != nil {
			t.Fatal(err)
		}
	}
}
