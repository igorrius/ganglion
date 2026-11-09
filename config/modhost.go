package config

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// ModuleHost provides the host of the owner's modules, started: the active
// local modules run with their cells, cron jobs and tools before the agent
// takes its first turn (R-RUN-11..14). Their state is in the database;
// their secrets come from the node's source; their off-switches are the
// settings file's modules: lines.
func ModuleHost() do.Provider[*modhost.Host] {
	return func(i do.Injector) (*modhost.Host, error) {
		b, logger := do.MustInvoke[Bootstrap](i), do.MustInvoke[*slog.Logger](i)
		h := modhost.New(modhost.Deps{
			Store:    do.MustInvoke[*modstore.Store](i),
			Cells:    do.MustInvoke[*cells.Service](i),
			Jobs:     do.MustInvoke[*schedule.Store](i),
			Inbox:    do.MustInvoke[*sqlqueue.Queue](i),
			Secrets:  moduleSecrets(b),
			State:    runner.NewSQLState(do.MustInvoke[Database](i).DB, runner.WithStateLogger(logger)),
			Settings: settings.ModulesFile{Path: settingsFile(b)},
			Admins:   do.MustInvoke[alert.Admins](i),
			Now:      time.Now,
			Log:      logger,
		})
		return h, h.Start(do.MustInvoke[Startup](i))
	}
}

// moduleSecrets is the node's secret source, or none when the command line
// gave none (a test node).
func moduleSecrets(b Bootstrap) secrets.Source {
	if b.Secrets == nil {
		return secrets.None{}
	}
	return b.Secrets
}

// settingsFile is the settings file the node edits: the one it read, or
// ganglion.yaml beside the database.
func settingsFile(b Bootstrap) string {
	if b.SettingsPath != "" {
		return b.SettingsPath
	}
	return filepath.Join(filepath.Dir(b.DB), settings.FileName)
}
