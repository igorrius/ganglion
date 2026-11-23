package config

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// Bus provides the node's message bus (R-BUS-1): the embedded server or the
// connection to an external one, with the bucket and the streams of R-BUS-2
// in place, on the trust chain from the secret store (ADR-0113). A bus that
// cannot be opened stops the start. It depends on the database, so the
// container shuts it down first.
func Bus() do.Provider[*natsbus.Bus] {
	return func(i do.Injector) (*natsbus.Bus, error) {
		do.MustInvoke[Database](i)
		return openBus(do.MustInvoke[Startup](i), do.MustInvoke[Bootstrap](i), do.MustInvoke[*vault.Vault](i), do.MustInvoke[*slog.Logger](i))
	}
}

// openBus opens the bus of the node b and makes its bucket and streams. It is
// the one place that does, for the node and for a backup made without one.
func openBus(ctx context.Context, b Bootstrap, v *vault.Vault, log *slog.Logger) (*natsbus.Bus, error) {
	cfg := busConfig(b)
	if b.EdgeRole {
		l, err := edgeLeaf(b)
		if err != nil {
			return nil, err
		}
		cfg.Leaf = l // an edge's own server has no chain: the chain stays on the core
	} else if err := withChain(ctx, &cfg, b, v); err != nil {
		return nil, err
	}
	nb, err := natsbus.Open(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	if err := nb.Ensure(ctx); err != nil {
		_ = nb.Shutdown(ctx)
		return nil, err
	}
	return nb, nil
}

// busConfig maps the bootstrap's bus settings to the bus's configuration. A
// bootstrap made in code that sets none gets the embedded server.
func busConfig(b Bootstrap) natsbus.Config {
	cfg := natsbus.DefaultConfig(b.BusDir)
	if s := b.Settings.Bus; s.Mode() == bus.ModeExternal {
		cfg.Mode, cfg.URLs, cfg.Credentials = bus.ModeExternal, s.URLs, b.BusCredentials
	}
	return cfg
}

// withChain loads the trust chain into cfg when the bus runs on it: always
// for the embedded server, and for an external one with bus.trust: chain. A
// locked store leaves the chain out and marks it (ADR-0113).
func withChain(ctx context.Context, cfg *natsbus.Config, b Bootstrap, v *vault.Vault) error {
	if cfg.Mode == bus.ModeExternal && b.Settings.Bus.Trust != settings.TrustChain {
		return nil
	}
	c, err := natsbus.LoadChain(ctx, v.Internal(), v.Locked, time.Now)
	switch {
	case errors.Is(err, natsbus.ErrStoreLocked) && cfg.Mode == bus.ModeEmbedded:
		cfg.ChainLocked = true
		return nil
	case err != nil:
		return err
	}
	cfg.Chain = c
	// The edges' entry is opened only for an embedded server that has a
	// public listener to be reached through (ADR-0114).
	cfg.WebSocket = cfg.Mode == bus.ModeEmbedded && httpOn(b)
	return nil
}
