package builder

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
	cfg := LocalFromEnv(func(string) string { return "" })
	if cfg.Cargo != filepath.Join(bin, "cargo") || cfg.Rustup != filepath.Join(home, ".rustup") {
		t.Fatalf("cargo %q, rustup %q", cfg.Cargo, cfg.Rustup)
	}
	cfg = LocalFromEnv(func(k string) string {
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
	if cfg := LocalFromEnv(func(string) string { return "" }); cfg.Cargo != "" {
		t.Fatalf("cargo = %q, want none", cfg.Cargo)
	}
}

// rustcPrinting writes a rustc into bin that prints sysroot.
func rustcPrinting(t *testing.T, bin, sysroot string) {
	t.Helper()
	script := "#!/bin/sh\necho " + sysroot + "\n"
	if err := os.WriteFile(filepath.Join(bin, "rustc"), []byte(script), 0o700); err != nil { //nolint:gosec // a fake rustc
		t.Fatal(err)
	}
}

// Builds are pinned to the toolchain rustc names at start (ADR-0064); a
// sysroot outside rustup's toolchains pins nothing, and the node builds
// nothing.
func TestLocalBuildPinsTheToolchain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))
	bin := filepath.Join(home, ".cargo", "bin")
	toolchains := filepath.Join(home, ".rustup", "toolchains", "stable-x86_64-unknown-linux-gnu")
	for _, d := range []string{bin, toolchains} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "cargo"), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // a fake cargo
		t.Fatal(err)
	}
	rustcPrinting(t, bin, toolchains)
	if cfg := LocalFromEnv(func(string) string { return "" }); cfg.Toolchain != "stable-x86_64-unknown-linux-gnu" {
		t.Fatalf("toolchain = %q", cfg.Toolchain)
	}
	rustcPrinting(t, bin, t.TempDir())
	if cfg := LocalFromEnv(func(string) string { return "" }); cfg.Toolchain != "" {
		t.Fatalf("a sysroot outside rustup pinned %q", cfg.Toolchain)
	}
}
