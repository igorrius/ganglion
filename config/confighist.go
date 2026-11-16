package config

import (
	"errors"
	"log/slog"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// FileHistory provides the record of the node's file (R-CFG-13,
// ADR-0093): a copy in the workspace repository, committed by every
// writer. Building it commits a file that changed on the host; the module
// host builds it at start. A node without git, or without a workspace (one
// with no agent), keeps no history.
func FileHistory() do.Provider[confighist.History] {
	return func(i do.Injector) (confighist.History, error) {
		if do.MustInvoke[Bootstrap](i).Agent.Workspace == "" {
			return confighist.NoHistory{}, nil
		}
		ws, err := do.Invoke[*gitrepo.Repo](i)
		if errors.Is(err, gitrepo.ErrNoGit) {
			return confighist.NoHistory{}, nil
		}
		if err != nil {
			return nil, err
		}
		h := confighist.New(settingsFile(do.MustInvoke[Bootstrap](i)), ws, do.MustInvoke[*slog.Logger](i))
		return h, h.Sync(do.MustInvoke[Startup](i))
	}
}

// modulesFile is the modules: lines of the node's file, every change
// recorded.
func modulesFile(i do.Injector) confighist.ModulesFile {
	return confighist.ModulesFile{File: settings.ModulesFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))},
		History: do.MustInvoke[confighist.History](i)}
}

// destinationsFile is the destinations of the node's file, every change
// recorded.
func destinationsFile(i do.Injector) confighist.DestinationsFile {
	return confighist.DestinationsFile{File: settings.DestinationsFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))},
		History: do.MustInvoke[confighist.History](i)}
}
