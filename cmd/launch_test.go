package cmd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/update"
)

func TestRunWithoutAStateCreatesNoReleasesFolder(t *testing.T) {
	clearEnv(t)
	var out syncBuffer
	args := nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")
	started := false
	start := func(context.Context, []node.Role, config.Bootstrap, *slog.Logger) error { started = true; return nil }
	if err := newRoot(&out, &out, start).Run(context.Background(), append([]string{"ganglion", "run"}, args...)); err != nil || !started {
		t.Fatalf("%v %v", err, started)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(args[1]), "releases")); err == nil {
		t.Fatal("the releases folder was created")
	}
}

// Abuse: a state naming a release that was planted is never run; the node
// starts as it is and the state records the fall back.
func TestRunGivesUpAPlantedRelease(t *testing.T) {
	clearEnv(t)
	withImageVersion(t, "v0.2.1")
	var out syncBuffer
	args := nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")
	dir := filepath.Join(filepath.Dir(args[1]), "releases")
	if err := os.MkdirAll(filepath.Join(dir, "v9.9.9"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v9.9.9", update.BinaryFile), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, update.StateFile), []byte(`{"active":"v9.9.9","previous":"","confirmed":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started := false
	start := func(context.Context, []node.Role, config.Bootstrap, *slog.Logger) error { started = true; return nil }
	if err := newRoot(&out, &out, start).Run(context.Background(), append([]string{"ganglion", "run"}, args...)); err != nil || !started {
		t.Fatalf("%v %v", err, started)
	}
	b, _ := os.ReadFile(filepath.Join(dir, update.StateFile))
	if !strings.Contains(string(b), `"reason": "unverified"`) || !strings.Contains(out.String(), "v9.9.9 is given up") {
		t.Fatalf("state %s\nlog %s", b, out.String())
	}
}

// withImageVersion makes this binary report version to the launcher.
func withImageVersion(t *testing.T, version string) {
	t.Helper()
	was := imageVersion
	imageVersion = func() string { return version }
	t.Cleanup(func() { imageVersion = was })
}

// A newer image deployed over an installed release runs, and the stored
// releases go (#279, ADR-0102).
func TestRunOfANewerImageDropsTheReleases(t *testing.T) {
	clearEnv(t)
	withImageVersion(t, "dev")
	var out syncBuffer
	args := nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")
	dir := filepath.Join(filepath.Dir(args[1]), "releases")
	if err := os.MkdirAll(filepath.Join(dir, "v0.6.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, update.StateFile), []byte(`{"active":"v0.6.0","previous":"","confirmed":"v0.6.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started := false
	start := func(context.Context, []node.Role, config.Bootstrap, *slog.Logger) error { started = true; return nil }
	if err := newRoot(&out, &out, start).Run(context.Background(), append([]string{"ganglion", "run"}, args...)); err != nil || !started {
		t.Fatalf("%v %v", err, started)
	}
	b, _ := os.ReadFile(filepath.Join(dir, update.StateFile))
	if !strings.Contains(string(b), `"active": ""`) || !strings.Contains(out.String(), "not older than the release v0.6.0") {
		t.Fatalf("state %s\nlog %s", b, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "v0.6.0")); err == nil {
		t.Fatal("the release folder stays")
	}
}

func TestRunWithAnUnreadableStateStartsAsItIs(t *testing.T) {
	clearEnv(t)
	var out syncBuffer
	args := nodeFiles(t, "metrics: {addr: \"127.0.0.1:0\"}\nlog: {dir: off}\n")
	dir := filepath.Join(filepath.Dir(args[1]), "releases")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, update.StateFile), []byte(`{"active":"../x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started := false
	start := func(context.Context, []node.Role, config.Bootstrap, *slog.Logger) error { started = true; return nil }
	if err := newRoot(&out, &out, start).Run(context.Background(), append([]string{"ganglion", "run"}, args...)); err != nil || !started {
		t.Fatalf("%v %v", err, started)
	}
	if !strings.Contains(out.String(), "the launcher failed") {
		t.Fatalf("log %s", out.String())
	}
}

func TestCleanStopTakesTheAttemptBackOfTheRunningVersion(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ganglion.db")
	dir := filepath.Join(filepath.Dir(db), "releases")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"active":"v0.3.0","previous":"","pending":{"version":"v0.3.0","attempts":1,"since":"2026-11-15T12:00:00Z"},"confirmed":""}`
	if err := os.WriteFile(filepath.Join(dir, update.StateFile), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	cleanStop(db, buildinfo.Info{Version: "v0.3.0"}, &out)
	store, err := update.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.State()
	if err != nil || st.Pending.Attempts != 0 || out.String() != "" {
		t.Fatalf("%+v %v %q", st, err, out.String())
	}
}

func TestMigrationsListsWhatADatabaseHasApplied(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (component TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT, PRIMARY KEY (component, version))`,
		`INSERT INTO schema_migrations (component, version) VALUES ('queue', 1), ('queue', 2), ('turns', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	got, err := migrations(ctx, path)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, []string{"queue:1", "queue:2", "turns:1"}) {
		t.Fatalf("%v %v", got, err)
	}
	empty := filepath.Join(t.TempDir(), "b.db")
	db, err = sqlitedb.Open(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if got, err := migrations(ctx, empty); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
