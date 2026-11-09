package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalBuildNamesTheToolchainFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))
	bin := filepath.Join(home, ".cargo", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "cargo"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := localBuild(func(string) string { return "" })
	if cfg.Cargo != filepath.Join(bin, "cargo") || cfg.Rustup != filepath.Join(home, ".rustup") {
		t.Fatalf("cargo %q, rustup %q", cfg.Cargo, cfg.Rustup)
	}
	cfg = localBuild(func(k string) string {
		if k == "RUSTUP_HOME" {
			return "/opt/rustup"
		}
		return ""
	})
	if cfg.Rustup != "/opt/rustup" || cfg.Timeout == 0 || cfg.MaxCopy == 0 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLocalBuildWithNoCargoBuildsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if cfg := localBuild(func(string) string { return "" }); cfg.Cargo != "" {
		t.Fatalf("cargo = %q, want none", cfg.Cargo)
	}
}
