package config

import (
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
		ctx := do.MustInvoke[Startup](i)
		b, err := natsbus.Open(ctx, busConfig(do.MustInvoke[Bootstrap](i)), do.MustInvoke[*slog.Logger](i))
		if err != nil {
			return nil, err
		}
		if err := b.Ensure(ctx); err != nil {
			_ = b.Shutdown(ctx)
			return nil, err
		}
		return b, nil
	}
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
