package cmd

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/igorrius/ganglion/builder"
)

// localBuild is what the module builder is told: cargo from the PATH or from
// rustup's own folder, and rustup's home from RUSTUP_HOME or ~/.rustup. The
// build runs with a temporary HOME, so the toolchain folder must be named.
// No cargo leaves Cargo empty: the node builds nothing.
func localBuild(getenv func(string) string) builder.LocalConfig {
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
	return cfg
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
