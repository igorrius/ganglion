package config

import (
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/status/proc"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// StatusSources provides the components /status asks (R-OBS-11): each part
// that is on adds its own.
func StatusSources() do.Provider[[]status.Source] {
	return func(i do.Injector) ([]status.Source, error) {
		b := do.MustInvoke[Bootstrap](i)
		return []status.Source{
			proc.New(time.Time(do.MustInvoke[ProcessStart](i)), proc.Files{DB: b.DB, Logs: b.LogDir}),
			do.MustInvoke[*turns.Log](i),
		}, nil
	}
}
