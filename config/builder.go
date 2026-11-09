package config

import (
	"os"
	"path/filepath"

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
// 0700, under the user's cache folder, where every folder above is the
// user's or root's and no one else's to write (ADR-0064). Not the shared
// temp folder, where anyone can plant a cargo configuration in a parent,
// and not the data folder, which the sandbox denies. "" when the user has
// no cache folder: the node builds nothing then.
func buildRoot() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "ganglion", "builds")
}
