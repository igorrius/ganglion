//go:build linux

package builder

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// diskDir is a folder on a real filesystem: the kernel keeps a tmpfs file's
// pages whatever it is advised, and t.TempDir is often on tmpfs.
func diskDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder:", err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Skip(err)
	}
	dir, err := os.MkdirTemp(cache, "ganglion-pagecache-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil || st.Type == unix.TMPFS_MAGIC {
		t.Skip("the cache folder is on tmpfs")
	}
	return dir
}

// cachedFile writes a file of size bytes, flushes it and reads it back, so
// its pages are clean and in the kernel's file cache.
func cachedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
}

// cachedPages counts the pages of the file the kernel holds in memory.
func cachedPages(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, int(info.Size()), unix.PROT_NONE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Munmap(mem) }()
	vec := make([]byte, (len(mem)+os.Getpagesize()-1)/os.Getpagesize())
	if _, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&mem[0])), uintptr(len(mem)), uintptr(unsafe.Pointer(&vec[0]))); errno != 0 {
		t.Fatal(errno)
	}
	n := 0
	for _, b := range vec {
		n += int(b & 1)
	}
	return n
}

func TestABuildGivesBackTheToolchainsFileCache(t *testing.T) {
	buildTemp(t)
	rustup := diskDir(t)
	cfg := testConfig()
	cfg.Rustup = rustup
	lib := filepath.Join(rustup, "toolchains", cfg.Toolchain, "lib", "librustc_driver.so")
	other := filepath.Join(rustup, "toolchains", "another", "lib", "lib.so")
	cachedFile(t, lib, 1<<20)
	cachedFile(t, other, 1<<20)
	if cachedPages(t, lib) == 0 {
		t.Skip("the kernel did not cache the file")
	}
	fake := &fakeStarter{wasm: []byte("\x00asm\x01\x00\x00\x00")}
	if _, _, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)}); err != nil {
		t.Fatal(err)
	}
	if n := cachedPages(t, lib); n != 0 {
		t.Fatalf("%d pages of the toolchain are still cached after the build", n)
	}
	if cachedPages(t, other) == 0 {
		t.Fatal("a toolchain the build did not use lost its cache")
	}
}

func TestAFailedBuildGivesBackTheCacheToo(t *testing.T) {
	buildTemp(t)
	rustup := diskDir(t)
	cfg := testConfig()
	cfg.Rustup = rustup
	lib := filepath.Join(rustup, "toolchains", cfg.Toolchain, "lib", "librustc_driver.so")
	cachedFile(t, lib, 1<<20)
	if cachedPages(t, lib) == 0 {
		t.Skip("the kernel did not cache the file")
	}
	fake := &fakeStarter{exit: "exit 1"}
	if _, _, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)}); err == nil {
		t.Fatal("the build passed")
	}
	if n := cachedPages(t, lib); n != 0 {
		t.Fatalf("%d pages of the toolchain are still cached after the failed build", n)
	}
}

func TestDropFileCacheSkipsWhatItCannotRead(t *testing.T) {
	dir := diskDir(t)
	if err := os.Symlink("/nonexistent", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	dropFileCache(filepath.Join(dir, "missing"))
	dropFileCache(dir)
}
