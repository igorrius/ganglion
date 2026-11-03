package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func (c *clock) step(d time.Duration) { c.t = c.t.Add(d) }

func open(t *testing.T, cfg Config, c *clock) *Rotator {
	t.Helper()
	r, err := Open(cfg, c.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func line(n int) []byte { return append(bytes.Repeat([]byte("x"), n-1), '\n') }

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig("/data/logs")
	if c.Dir != "/data/logs" || c.MaxBytes != 10<<20 || c.MaxFiles != 5 || c.MaxAge != 14*24*time.Hour {
		t.Fatalf("%+v", c)
	}
}

func TestOpenMakesPrivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	r := open(t, DefaultConfig(dir), &clock{time.Unix(1_800_000_000, 0)})
	if _, err := r.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode().Perm())
	}
	st, err := os.Stat(filepath.Join(dir, CurrentName))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %v, %v", st, err)
	}
}

func TestRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Date(2026, 11, 3, 10, 15, 0, 0, time.UTC)}
	r := open(t, Config{Dir: dir, MaxBytes: 100, MaxFiles: 5, MaxAge: time.Hour}, c)
	for range 3 {
		if _, err := r.Write(line(40)); err != nil {
			t.Fatal(err)
		}
		c.step(time.Millisecond)
	}
	got := names(t, dir)
	want := []string{"ganglion-20261103T101500.002Z.log", CurrentName}
	if !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, CurrentName))
	old, _ := os.ReadFile(filepath.Join(dir, want[0]))
	if len(cur) != 40 || len(old) != 80 {
		t.Fatalf("current %d bytes, rotated %d", len(cur), len(old))
	}
}

// Review focus 3: a burst of lines never keeps more than MaxFiles
// rotated files.
func TestKeepsAtMostMaxFiles(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Unix(1_800_000_000, 0)}
	r := open(t, Config{Dir: dir, MaxBytes: 64 << 10, MaxFiles: 5, MaxAge: 24 * time.Hour}, c)
	for range 10_000 {
		if _, err := r.Write(line(1024)); err != nil {
			t.Fatal(err)
		}
		c.step(time.Millisecond)
	}
	if n := len(names(t, dir)); n > 6 {
		t.Fatalf("%d files: %v", n, names(t, dir))
	}
}

func TestPrunesByAge(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Unix(1_800_000_000, 0)}
	r := open(t, Config{Dir: dir, MaxBytes: 10, MaxFiles: 100, MaxAge: time.Hour}, c)
	_, _ = r.Write(line(20)) // fills the first file
	_, _ = r.Write(line(20)) // rotates it
	c.step(2 * time.Hour)
	_, _ = r.Write(line(20)) // rotates again: the first rotated file is now too old
	got := names(t, dir)
	if len(got) != 2 || got[1] != CurrentName {
		t.Fatalf("files %v", got)
	}
	if !strings.HasPrefix(got[0], "ganglion-") {
		t.Fatalf("files %v", got)
	}
}

func TestPruneTouchesOnlyOwnFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"notes.txt", "ganglion.db", "ganglion-backup.tar", "ganglion-old.log.gz"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &clock{time.Unix(1_800_000_000, 0)}
	r := open(t, Config{Dir: dir, MaxBytes: 10, MaxFiles: 0, MaxAge: time.Nanosecond}, c)
	for range 5 {
		_, _ = r.Write(line(20))
		c.step(time.Second)
	}
	got := names(t, dir)
	for _, n := range []string{"notes.txt", "ganglion.db", "ganglion-backup.tar", "ganglion-old.log.gz"} {
		if !slices.Contains(got, n) {
			t.Fatalf("%s removed: %v", n, got)
		}
	}
}

// Open after a restart appends to the current file rather than losing it.
func TestReopenAppends(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Unix(1_800_000_000, 0)}
	r1, err := Open(DefaultConfig(dir), c.now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r1.Write([]byte("one\n"))
	_ = r1.Close()
	r2 := open(t, DefaultConfig(dir), c)
	_, _ = r2.Write([]byte("two\n"))
	if b, _ := os.ReadFile(filepath.Join(dir, CurrentName)); string(b) != "one\ntwo\n" {
		t.Fatalf("file %q", b)
	}
}
