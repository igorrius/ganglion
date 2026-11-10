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
	for _, p := range []string{"home/.ssh", "data/logs", "data/modules", "ws", "keys", "bin"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"data/ganglion.db", "keys/bot", "home/.netrc", "bin/ganglion"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	node := NodeFiles{Binary: filepath.Join(dir, "bin/ganglion"), DB: filepath.Join(dir, "data/ganglion.db"),
		LogDir: filepath.Join(dir, "data/logs"), ModulesDir: filepath.Join(dir, "data/modules"), ClaudeDir: filepath.Join(dir, "elsewhere/claude"), Workspace: filepath.Join(dir, "ws")}
	environ := []string{"PATH=/bin", "TELEGRAM_BOT_TOKEN_FILE=" + filepath.Join(dir, "keys/bot"), "X_FILE="}
	got := DenyList(home, dir, node, environ, []string{filepath.Join(dir, "missing/later")})
	for _, want := range []string{"bin/ganglion", "data", "ws", "keys/bot", "home/.ssh", "home/.netrc",
		"elsewhere/claude", "home/.aws", "home/.bashrc", "home/.config/systemd", "missing/later"} {
		if !slices.Contains(got, filepath.Join(dir, want)) {
			t.Errorf("%s is not denied; got %v", want, got)
		}
	}
}

// TestDenyListKeepsADatabaseBesideOtherWork: a database in the working
// folder denies itself and its journal files, not the folder.
func TestDenyListKeepsADatabaseBesideOtherWork(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := DenyList("", dir, NodeFiles{DB: filepath.Join(dir, "ganglion.db")}, nil, nil)
	for _, want := range []string{"ganglion.db", "ganglion.db-wal", "ganglion.db-shm", "ganglion.db-journal", "ganglion.db.lock"} {
		if !slices.Contains(got, filepath.Join(dir, want)) {
			t.Errorf("%s is not denied; got %v", want, got)
		}
	}
	if slices.Contains(got, dir) {
		t.Fatalf("the working folder is denied: %v", got)
	}
}

// TestDenyListHoldsTheSettingsAndTheLockInTheImagesLayout: the container
// image runs in /data, where the database and ganglion.yaml are. A command
// that rewrote ganglion.yaml would choose the admins and the providers'
// commands at the next restart; one that held the lock would keep the node
// from starting. Both are denied though the folder is not.
func TestDenyListHoldsTheSettingsAndTheLockInTheImagesLayout(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := NodeFiles{DB: filepath.Join(data, "ganglion.db"), Settings: filepath.Join(data, "ganglion.yaml"),
		Workspace: filepath.Join(data, "workspace")}
	got := DenyList("", data, node, nil, nil)
	for _, want := range []string{"ganglion.yaml", "ganglion.db.lock", "ganglion.db", "workspace"} {
		if !slices.Contains(got, filepath.Join(data, want)) {
			t.Errorf("%s is not denied; got %v", want, got)
		}
	}
	if slices.Contains(got, data) {
		t.Fatalf("the working folder is denied: %v", got)
	}
}

// A settings file named with --config anywhere is denied too.
func TestDenyListHoldsASettingsFileElsewhere(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "etc", "node.yaml")
	got := DenyList("", "/", NodeFiles{DB: filepath.Join(dir, "data", "ganglion.db"), Settings: settings}, nil, nil)
	if !slices.Contains(got, settings) {
		t.Fatalf("the settings file is not denied: %v", got)
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
	got := DenyList("", dir, NodeFiles{}, nil, []string{filepath.Join(dir, "link")})
	if !slices.Contains(got, target) {
		t.Fatalf("got %v, want the symlink's target %s", got, target)
	}
}

func TestEnvFilesFindsThemToADepth(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a/.env", "a/.env.local", "a/.envrc", "a/b/c/d/e/.env", "a/node_modules/x/.env", "a/env"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "a/env"), filepath.Join(dir, "a/b/.env")); err != nil {
		t.Fatal(err)
	}
	got := EnvFiles([]string{dir}, 3, os.ReadDir)
	want := []string{filepath.Join(dir, "a/.env"), filepath.Join(dir, "a/.env.local"), filepath.Join(dir, "a/.envrc"),
		filepath.Join(dir, "a/b/.env")}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
