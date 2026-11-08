package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDenyListHoldsTheNodesFilesAndSecrets(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // DenyList answers with real paths
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	for _, p := range []string{"home/.ssh", "data/logs", "data/modules", "ws", "keys"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"data/ganglion.db", "data/ganglion.db-wal", "keys/bot", "home/.netrc"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	node := NodeFiles{DB: filepath.Join(dir, "data/ganglion.db"), LogDir: filepath.Join(dir, "data/logs"),
		ModulesDir: filepath.Join(dir, "data/modules"), Workspace: filepath.Join(dir, "ws")}
	environ := []string{"PATH=/bin", "TELEGRAM_BOT_TOKEN_FILE=" + filepath.Join(dir, "keys/bot"), "X_FILE="}
	got := DenyList(home, node, environ, []string{filepath.Join(dir, "missing")})
	for _, want := range []string{"data/ganglion.db", "data/ganglion.db-wal", "data/logs", "data/modules", "ws",
		"keys/bot", "home/.ssh", "home/.netrc"} {
		if !slices.Contains(got, filepath.Join(dir, want)) {
			t.Errorf("%s is not denied; got %v", want, got)
		}
	}
	for _, p := range got {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s does not exist and should be left out", p)
		}
	}
}

func TestDenyListResolvesSymlinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got := DenyList(dir, NodeFiles{}, nil, []string{filepath.Join(dir, "link")})
	if !slices.Contains(got, target) {
		t.Fatalf("got %v, want the symlink's target %s", got, target)
	}
}

func TestEnvFilesFindsThemToADepth(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a/.env", "a/.env.local", "a/b/c/d/e/.env", "a/node_modules/x/.env", "a/env"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := EnvFiles([]string{dir}, 3, os.ReadDir)
	want := []string{filepath.Join(dir, "a/.env"), filepath.Join(dir, "a/.env.local")}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
