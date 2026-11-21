package config

import (
	"context"
	"log/slog"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/samber/do/v2"
)

// Bus provides the node's message bus (R-BUS-1): the embedded server or the
// connection to an external one, with the bucket and the streams of R-BUS-2
// in place. A bus that cannot be opened stops the start. It depends on the
// database, so the container shuts it down first.
func Bus() do.Provider[*natsbus.Bus] {
	return func(i do.Injector) (*natsbus.Bus, error) {
		do.MustInvoke[Database](i)
		return openBus(do.MustInvoke[Startup](i), do.MustInvoke[Bootstrap](i), do.MustInvoke[*slog.Logger](i))
	}
}

// openBus opens the bus of the node b and makes its bucket and streams. It is
// the one place that does, for the node and for a backup made without one.
func openBus(ctx context.Context, b Bootstrap, log *slog.Logger) (*natsbus.Bus, error) {
	nb, err := natsbus.Open(ctx, busConfig(b), log)
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
