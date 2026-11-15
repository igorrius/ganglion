package config

import (
	"context"
	"log/slog"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/internal/ghfetch"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/update"
	"github.com/samber/do/v2"
)

// ReleaseStore provides the node's folder of core releases, beside the
// database (R-UPD-2), which copies the node's database for a release to
// fall back to. It is built only by the parts that need it: the update
// watch, and an install or a rollback.
func ReleaseStore() do.Provider[*update.Store] {
	return func(i do.Injector) (*update.Store, error) {
		b := do.MustInvoke[Bootstrap](i)
		dir, _ := update.DirFor(b.DB)
		db := do.MustInvoke[Database](i).DB
		return update.Open(dir, update.WithLogger(do.MustInvoke[*slog.Logger](i)), update.WithImageEnv(b.ImageEnv),
			update.WithCopyDB(func(ctx context.Context, dst string) error { return sqlitedb.CopyTo(ctx, db, dst) }))
	}
}

// Updater provides what /update uses: the GitHub releases and the release
// store for a node whose database is a file, and a refusal for one that
// keeps nothing on disk.
func Updater() do.Provider[agent.Updater] {
	return choose[agent.Updater](updaterOn, updaterReleases, updaterNone)
}

// updaterOn reports whether the node has a releases folder to keep.
func updaterOn(b Bootstrap) bool {
	_, ok := update.DirFor(b.DB)
	return ok
}

// UpdaterReleases provides the updater over GitHub. It opens the store, and
// so makes the releases folder, only when something is installed.
func UpdaterReleases() do.Provider[agent.Updater] {
	return func(i do.Injector) (agent.Updater, error) {
		b := do.MustInvoke[Bootstrap](i)
		dir, _ := update.DirFor(b.DB)
		open := func() (*update.Store, error) { return do.Invoke[*update.Store](i) }
		return update.NewUpdater(update.NewSource(ghfetch.New()), dir, open, buildinfo.Read().Version,
			update.WithImage(b.ImageEnv > 0)), nil
	}
}

// Updates are the components that watch the core releases.
type Updates []node.Component

// UpdateComponents provides the update watch when the node has a releases
// state, and nothing for a node that never updated.
func UpdateComponents() do.Provider[Updates] {
	return choose[Updates](updateOn, updateWatch, updateNone)
}

// updateOn reports whether the node has a release state to watch.
func updateOn(b Bootstrap) bool {
	dir, ok := update.DirFor(b.DB)
	return ok && update.HasState(dir)
}

// UpdateWatch provides the component that confirms a new version and
// tells the admins what the launcher did.
func UpdateWatch() do.Provider[Updates] {
	return func(i do.Injector) (Updates, error) {
		w := update.NewWatch(do.MustInvoke[*update.Store](i), buildinfo.Read().Version, do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Enqueue, do.MustInvoke[*language.Store](i).Resolve,
			do.MustInvoke[*texts.Catalog](i), do.MustInvoke[*slog.Logger](i))
		return Updates{w}, nil
	}
}

// UpdateChecks are the components that look for a newer core release.
type UpdateChecks []node.Component

// UpdateCheckComponents provides the daily check when update.check is on
// and the node keeps its database in a file, and nothing otherwise.
func UpdateCheckComponents() do.Provider[UpdateChecks] {
	return choose[UpdateChecks](updateCheckOn, updateCheck, updateCheckNone)
}

// updateCheckOn reports whether the node asks for a newer release: the
// setting is on and there is a releases folder to keep the record in.
func updateCheckOn(b Bootstrap) bool {
	_, ok := update.DirFor(b.DB)
	return ok && b.UpdateCheck
}

// UpdateCheck provides the daily check for a newer release; it tells the
// admins and never installs.
func UpdateCheck() do.Provider[UpdateChecks] {
	return func(i do.Injector) (UpdateChecks, error) {
		b := do.MustInvoke[Bootstrap](i)
		dir, _ := update.DirFor(b.DB)
		opts := []update.CheckerOption{update.WithCheckButtons(do.MustInvoke[*pending.Store](i))}
		if b.Agent.TZ != nil {
			opts = append(opts, update.WithCheckLocation(b.Agent.TZ))
		}
		c := update.NewChecker(update.NewSource(ghfetch.New()), dir, buildinfo.Read().Version, do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Enqueue, do.MustInvoke[*language.Store](i).Resolve,
			do.MustInvoke[*texts.Catalog](i), do.MustInvoke[*slog.Logger](i), opts...)
		return UpdateChecks{c}, nil
	}
}
