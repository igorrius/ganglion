package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// Builder provides the module builder: Rust in the command sandbox, from a
// temporary copy of the source (R-RUN-9, ADR-0061). With the sandbox off, or
// no cargo on the node, a build is refused with the reason.
func Builder() do.Provider[builder.Builder] {
	return func(i do.Injector) (builder.Builder, error) {
		return builder.NewLocal(do.MustInvoke[*sandbox.Sandbox](i), buildConfig(do.MustInvoke[Bootstrap](i))), nil
	}
}

// buildConfig is the builder's configuration with its temp root filled.
func buildConfig(b Bootstrap) builder.LocalConfig {
	cfg := b.Build
	if cfg.TempRoot == "" {
		cfg.TempRoot = buildRoot()
	}
	return cfg
}

// buildRoot is the folder the node's builds are made in: its own, mode
// 0700, in the temp folder, named for the user, so build folders are not
// made in a folder every user shares (ADR-0064). Not under home, where the
// owner's ~/.cargo/config.toml would be a parent's cargo configuration, and
// not under the data folder, which the sandbox denies.
func buildRoot() string {
	return filepath.Join(os.TempDir(), "ganglion-builds-"+strconv.Itoa(os.Geteuid()))
}
