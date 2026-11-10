package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// The checks of a path come before the write; a symlink made between them
// still cannot take the write out of the module's folder, since every
// write goes through the folder's os.Root.
func TestAWriteCannotLeaveTheModuleThroughALateSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "blink"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "blink", "src")); err != nil {
		t.Fatal(err)
	}
	mods, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := mods.OpenRoot("blink")
	if err != nil {
		t.Fatal(err)
	}
	w := &pendingWrite{mods: mods, mod: mod, name: "blink", rel: filepath.Join("src", "lib.rs")}
	defer w.close()
	if err := w.replace([]byte("x")); err == nil {
		t.Fatal("a write went through a symlink out of the folder")
	}
	if err := w.mod.MkdirAll(filepath.Join("src", "sub"), 0o750); err == nil {
		t.Fatal("a folder was made through a symlink out of the folder")
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatalf("written outside: %v", es)
	}
	if _, err := os.Lstat(filepath.Join(dir, "blink", tempName)); !os.IsNotExist(err) {
		t.Fatalf("the temporary file was left: %v", err)
	}
}
