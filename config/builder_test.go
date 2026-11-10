package config

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// With the sandbox off, the provided builder refuses to build and says why;
// a Go source is still refused as a language with no builder.
func TestTheBuilderRefusesWithTheSandboxOff(t *testing.T) {
	b := agentConfig(t)
	b.Build = builder.DefaultLocalConfig()
	b.Build.Cargo, b.Build.Toolchain = "/nonexistent/cargo", "stable-test"
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	do.OverrideValue(i, sandbox.New("/nonexistent", sandbox.Policy{}, rootKernel{}, time.Now()))
	bl, err := do.Invoke[builder.Builder](i)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"Cargo.toml": "[package]\n", "manifest.yaml": "name: local/m\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = bl.Build(context.Background(), builder.Source{Name: "m", Dir: dir})
	if err == nil || errors.Is(err, builder.ErrNoBuilder) {
		t.Fatalf("Build with the sandbox off: %v", err)
	}
}

// Builds are made in the node's own root under the user's cache folder, or
// nowhere, and the janitor sweeps what a crash left there (ADR-0064).
func TestBuildsGoToTheNodesOwnRoot(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder here")
	}
	root := buildConfig(Bootstrap{}).TempRoot
	if root != filepath.Join(cache, "ganglion", "builds") {
		t.Fatalf("TempRoot = %s", root)
	}
	b := agentConfig(t)
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	places, err := do.Invoke[[]host.Place](i)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(places, func(p host.Place) bool {
		return p.Path == root && p.Prefix == "ganglion-build-" && p.MaxAge > 0
	}) {
		t.Fatalf("the janitor does not sweep %s", root)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if root := buildConfig(Bootstrap{}).TempRoot; root != "" {
		t.Fatalf("with no cache folder TempRoot = %s, want none (builds refused)", root)
	}
}

func TestTheJanitorSweepsBuildLeftovers(t *testing.T) {
	var found bool
	for _, p := range tempPrefixes {
		found = found || p == "ganglion-build-"
	}
	if !found {
		t.Fatal("ganglion-build- is not among the temp prefixes the janitor sweeps")
	}
}
