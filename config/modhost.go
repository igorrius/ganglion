package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
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
			Store:       do.MustInvoke[*modstore.Store](i),
			Cells:       do.MustInvoke[*cells.Service](i),
			Jobs:        do.MustInvoke[*schedule.Store](i),
			Inbox:       do.MustInvoke[*sqlqueue.Queue](i),
			Secrets:     moduleSecrets(b),
			NodeSecrets: nodeSecrets(b),
			State:       runner.NewSQLState(do.MustInvoke[Database](i).DB, runner.WithStateLogger(logger)),
			Settings:    settings.ModulesFile{Path: settingsFile(b)},
			Admins:      do.MustInvoke[alert.Admins](i),
			Now:         time.Now,
			Location:    b.Agent.TZ,
			Log:         logger,
		})
		h.Start(do.MustInvoke[Startup](i))
		return h, nil
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

// Moduling is the module tools the agent has: module.import, module.write,
// module.read, module.build, module.install and module.status, or none of
// them when the workspace has no git.
type Moduling struct {
	Tools []tool.Handler
}

// ModuleTools provides the tools that bring the owner's modules in: the
// sources in the workspace, the builder, the store, the node's secrets, the
// settings' modules: lines, the cells and the module host (R-RUN-9). A host
// without git has none of them; memory's warning says why (ADR-0033).
func ModuleTools() do.Provider[Moduling] {
	return func(i do.Injector) (Moduling, error) {
		ws, err := do.Invoke[*gitrepo.Repo](i)
		if errors.Is(err, gitrepo.ErrNoGit) {
			return Moduling{}, nil
		}
		if err != nil {
			return Moduling{}, err
		}
		b := do.MustInvoke[Bootstrap](i)
		return Moduling{Tools: builtin.NewModuleTools(builtin.Modules{
			Workspace:   ws,
			Builder:     do.MustInvoke[builder.Builder](i),
			Store:       do.MustInvoke[*modstore.Store](i),
			Secrets:     moduleSecrets(b),
			NodeSecrets: nodeSecrets(b),
			Settings:    settings.ModulesFile{Path: settingsFile(b)},
			Cells:       do.MustInvoke[*cells.Service](i),
			Host:        do.MustInvoke[*modhost.Host](i),
			Builds:      buildsDir(b),
			Examples:    examplesDir(b),
			Provenance:  provenanceDir(b),
			Now:         time.Now,
		})}, nil
	}
}

// buildsDir is where builds wait to be installed: builds/ in the modules
// folder, swept by the janitor (ruling 6 of M4b).
func buildsDir(b Bootstrap) string { return filepath.Join(b.ModulesDir, "builds") }

// provenanceDir is where the node records each module folder as its own
// tools left it: provenance/ in the modules folder, beside the builds and
// out of the command sandbox's reach (ruling 7 of M4c).
func provenanceDir(b Bootstrap) string { return filepath.Join(b.ModulesDir, "provenance") }

// imageExamples is where the container image keeps the example modules.
const imageExamples = "/app/examples/modules"

// examplesDir is the folder of example modules module.import copies from:
// the one the command line named, else the image's, else examples/modules
// in the folder the node runs in (a checkout); "" when there is none.
func examplesDir(b Bootstrap) string {
	candidates := []string{imageExamples, filepath.Join("examples", "modules")}
	if b.Examples != "" {
		candidates = []string{b.Examples}
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs
		}
	}
	return ""
}
