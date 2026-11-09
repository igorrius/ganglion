package config

import (
	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// Builder provides the module builder: Rust in the command sandbox, from a
// temporary copy of the source (R-RUN-9, ADR-0061). With the sandbox off, or
// no cargo on the node, a build is refused with the reason.
func Builder() do.Provider[builder.Builder] {
	return func(i do.Injector) (builder.Builder, error) {
		return builder.NewLocal(do.MustInvoke[*sandbox.Sandbox](i), do.MustInvoke[Bootstrap](i).Build), nil
	}
}
