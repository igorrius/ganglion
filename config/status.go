package config

import (
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/status/proc"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// StatusSources provides the components /status asks (R-OBS-11): each part
// that is on adds its own.
func StatusSources() do.Provider[[]status.Source] {
	return func(i do.Injector) ([]status.Source, error) {
		b := do.MustInvoke[Bootstrap](i)
		sources := []status.Source{
			proc.New(time.Time(do.MustInvoke[ProcessStart](i)), proc.Files{DB: b.DB, Logs: b.LogDir}),
			do.MustInvoke[*turns.Log](i),
		}
		return append(sources, do.MustInvoke[Watched](i)...), nil
	}
}

// Watched are the status sources of the transports that are on.
type Watched []status.Source

// TransportWatch provides the transports' status sources.
func TransportWatch() do.Provider[Watched] {
	return choose[Watched](telegramOn, watchTelegram, watchNone)
}

// TelegramWatch provides Telegram's polling health as a status source.
func TelegramWatch() do.Provider[Watched] {
	return func(i do.Injector) (Watched, error) { return Watched{do.MustInvoke[*telegram.Health](i)}, nil }
}
