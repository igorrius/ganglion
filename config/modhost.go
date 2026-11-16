package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/cells"
	templates "github.com/igorrius/ganglion/examples/modules"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/samber/do/v2"
)

// ModuleHost provides the host of the owner's modules, started: the active
// local modules run with their cells, cron jobs and tools before the agent
// takes its first turn (R-RUN-11..14). Their state is in the database;
// each reads its own scope of the secret store, then the node's
// environment (ADR-0077); their off-switches are the
// settings file's modules: lines.
func ModuleHost() do.Provider[*modhost.Host] {
	return func(i do.Injector) (*modhost.Host, error) {
		b, logger := do.MustInvoke[Bootstrap](i), do.MustInvoke[*slog.Logger](i)
		store := do.MustInvoke[*vault.Vault](i)
		h := modhost.New(modhost.Deps{
			Store:        do.MustInvoke[*modstore.Store](i),
			Cells:        do.MustInvoke[*cells.Service](i),
			Jobs:         do.MustInvoke[*schedule.Store](i),
			Inbox:        do.MustInvoke[*sqlqueue.Queue](i),
			Secrets:      secretsFor(b, store),
			Forget:       forgetSecrets(store),
			NodeSecrets:  nodeSecrets(b),
			State:        runner.NewSQLState(do.MustInvoke[Database](i).DB, runner.WithStateLogger(logger)),
			Settings:     modulesFile(i),
			Destinations: do.MustInvoke[groups.Destinations](i).Resolve,
			Admins:       do.MustInvoke[alert.Admins](i),
			Now:          time.Now,
			Location:     b.Agent.TZ,
			Log:          logger,
		})
		h.Start(do.MustInvoke[Startup](i))
		return h, nil
	}
}

// moduleSecrets is the node's environment source, or none when the command
// line gave none (a test node).
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
		examples, err := examplesDir(b)
		if err != nil {
			return Moduling{}, err
		}
		return Moduling{Tools: builtin.NewModuleTools(builtin.Modules{
			Workspace:   ws,
			Builder:     do.MustInvoke[builder.Builder](i),
			Store:       do.MustInvoke[*modstore.Store](i),
			Secrets:     builtin.ModuleSecrets(secretsFor(b, do.MustInvoke[*vault.Vault](i))),
			NodeSecrets: nodeSecrets(b),
			Settings:    modulesFile(i),
			Cells:       do.MustInvoke[*cells.Service](i),
			Host:        do.MustInvoke[*modhost.Host](i),
			Builds:      buildsDir(b),
			Examples:    examples,
			Provenance:  provenanceDir(b),
			Now:         time.Now,
			Queue:       do.MustInvoke[*builtin.BuildQueue](i),
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

// buildJobsDir is where the build queue keeps a record of each build and
// its answer: build-jobs/ in the modules folder, beside the builds.
func buildJobsDir(b Bootstrap) string { return filepath.Join(b.ModulesDir, "build-jobs") }

// examplesDir is the folder of module templates module.import copies from.
// A node ships its templates in its binary (R-RDR-1): each start writes them
// to templates/ in the modules folder, beside the builds and out of the
// command sandbox's reach, so they are always those of the running version.
// A folder named in Bootstrap replaces them, for a test or a lab; "" when
// that folder does not exist.
func examplesDir(b Bootstrap) (string, error) {
	if b.Examples != "" {
		abs, err := filepath.Abs(b.Examples)
		if err != nil {
			return "", fmt.Errorf("config: the templates folder %s: %w", b.Examples, err)
		}
		if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
			return "", nil
		}
		return abs, nil
	}
	dir := filepath.Join(b.ModulesDir, "templates")
	if err := templates.Write(dir); err != nil {
		return "", err
	}
	return dir, nil
}
