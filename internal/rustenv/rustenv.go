// Package rustenv finds the Rust toolchain a node builds modules with, from
// the environment its caller passes in: the builder package reads none.
package rustenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/builder"
)

// toolchainTimeout bounds asking rustc for its toolchain at start.
const toolchainTimeout = 30 * time.Second

// LocalFromEnv is what the module builder is told: cargo from the PATH or from
// rustup's own folder, rustup's home from RUSTUP_HOME or ~/.rustup, and the
// toolchain every build is pinned to (toolchain). The build runs with a
// temporary HOME, so the toolchain folder must be named. No cargo leaves
// Cargo empty: the node builds nothing.
func LocalFromEnv(getenv func(string) string) builder.LocalConfig {
	cfg := builder.DefaultLocalConfig()
	home, _ := os.UserHomeDir()
	if cargo, err := exec.LookPath("cargo"); err == nil {
		cfg.Cargo = cargo
	} else if home != "" {
		if own := filepath.Join(home, ".cargo", "bin", "cargo"); isFile(own) {
			cfg.Cargo = own
		}
	}
	if cfg.Rustup = getenv("RUSTUP_HOME"); cfg.Rustup == "" && home != "" {
		cfg.Rustup = filepath.Join(home, ".rustup")
	}
	if cfg.Cargo != "" {
		cfg.Toolchain = toolchain(filepath.Join(filepath.Dir(cfg.Cargo), "rustc"), cfg.Rustup, home, getenv("RUSTUP_TOOLCHAIN"))
	}
	return cfg
}

// toolchain is the name of the rustup toolchain rustc resolves to, asked
// once at start from "/", where no project's rust-toolchain file is found,
// so builds can be pinned to it with RUSTUP_TOOLCHAIN: a rust-toolchain
// file in a build's folders then cannot name another (ADR-0064). The
// node's own RUSTUP_TOOLCHAIN is kept. "" when rustc does not answer or its
// sysroot is not one of rustup's toolchains: the node builds nothing then.
func toolchain(rustc, rustupHome, home, pinned string) string {
	ctx, cancel := context.WithTimeout(context.Background(), toolchainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, rustc, "--print", "sysroot")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=" + filepath.Dir(rustc) + ":/usr/bin:/bin", "RUSTUP_HOME=" + rustupHome,
		"HOME=" + home, "RUSTUP_AUTO_INSTALL=0"}
	if pinned != "" {
		cmd.Env = append(cmd.Env, "RUSTUP_TOOLCHAIN="+pinned)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	sysroot, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	toolchains, err := filepath.EvalSymlinks(filepath.Join(rustupHome, "toolchains"))
	if err != nil || filepath.Dir(sysroot) != toolchains {
		return ""
	}
	return filepath.Base(sysroot)
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
