package config

import (
	"github.com/igorrius/ganglion/status"
	"github.com/samber/do/v2"
)

// StatusSources provides the components /status asks (R-OBS-11): each part
// that is on adds its own.
func StatusSources() do.Provider[[]status.Source] {
	return func(do.Injector) ([]status.Source, error) {
		return []status.Source{}, nil
	}
}
