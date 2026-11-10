package builder_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/builder"
)

// tree writes files (relative path → text) into a new folder.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func digestOf(t *testing.T, dir string) string {
	t.Helper()
	d, err := builder.SourceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var moduleFiles = map[string]string{
	"Cargo.toml":    "[package]\nname = \"m\"\n",
	"manifest.yaml": "name: m\n",
	"src/lib.rs":    "pub fn f() {}\n",
	"src/logic.rs":  "pub fn g() -> u32 { 1 }\n",
}

func TestTheDigestIsStable(t *testing.T) {
	a, b := digestOf(t, tree(t, moduleFiles)), digestOf(t, tree(t, moduleFiles))
	if a != b || len(a) != 64 {
		t.Fatalf("two folders of the same files: %s and %s", a, b)
	}
	dir := tree(t, moduleFiles)
	first := digestOf(t, dir)
	if digestOf(t, dir) != first {
		t.Fatal("one folder digested twice differs")
	}
}

func TestTheDigestFollowsEveryChange(t *testing.T) {
	base := digestOf(t, tree(t, moduleFiles))
	for name, change := range map[string]func(map[string]string){
		"a byte": func(f map[string]string) { f["src/logic.rs"] = "pub fn g() -> u32 { 2 }\n" },
		"a name": func(f map[string]string) { f["src/logic2.rs"] = f["src/logic.rs"]; delete(f, "src/logic.rs") },
		"a file": func(f map[string]string) { f["src/new.rs"] = "" },
		"a split": func(f map[string]string) {
			f["src/lib.rs"] = "pub fn f() {}\n" + f["src/logic.rs"]
			f["src/logic.rs"] = ""
		},
		"a folder": func(f map[string]string) { f["src/a/logic.rs"] = f["src/logic.rs"]; delete(f, "src/logic.rs") },
	} {
		t.Run(name, func(t *testing.T) {
			files := maps.Clone(moduleFiles)
			change(files)
			if digestOf(t, tree(t, files)) == base {
				t.Fatal("the digest did not change")
			}
		})
	}
}

// The digest is over what a build copies: the top-level target and .git
// folders are not part of it, but a nested one is.
func TestTheDigestIgnoresTargetAndGit(t *testing.T) {
	base := digestOf(t, tree(t, moduleFiles))
	files := maps.Clone(moduleFiles)
	files["target/release/m.wasm"] = "wasm"
	files[".git/HEAD"] = "ref: refs/heads/master\n"
	if digestOf(t, tree(t, files)) != base {
		t.Fatal("target/ or .git/ changed the digest")
	}
	files = maps.Clone(moduleFiles)
	files["src/target/x.rs"] = ""
	if digestOf(t, tree(t, files)) == base {
		t.Fatal("a nested target folder was skipped")
	}
}

func TestTheDigestRefusesASymlink(t *testing.T) {
	for name, link := range map[string]string{"a file": "src/evil.rs", "a folder": "src/sub"} {
		t.Run(name, func(t *testing.T) {
			dir := tree(t, moduleFiles)
			if err := os.Symlink("/etc", filepath.Join(dir, filepath.FromSlash(link))); err != nil {
				t.Fatal(err)
			}
			if _, err := builder.SourceDigest(dir); err == nil || !strings.Contains(err.Error(), filepath.FromSlash(link)) {
				t.Fatalf("error = %v, want one naming %s", err, link)
			}
		})
	}
	dir := tree(t, moduleFiles)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.SourceDigest(link); err == nil {
		t.Fatal("a symlinked folder was digested")
	}
}

// SourceFiles lists what the digest covers, in the order it walks.
func TestSourceFilesListsWhatTheDigestCovers(t *testing.T) {
	files := maps.Clone(moduleFiles)
	files["target/x"] = "x"
	files[".git/HEAD"] = "x"
	got, err := builder.SourceFiles(tree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	want := []string{"Cargo.toml", "manifest.yaml", "src/lib.rs", "src/logic.rs"}
	if !slices.Equal(paths, want) || got[2].Size != int64(len(moduleFiles["src/lib.rs"])) {
		t.Fatalf("files %+v, want %v", got, want)
	}
}
