package config

import (
	"log/slog"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/update"
	"github.com/samber/do/v2"
)

// ReleaseStore provides the node's folder of core releases, beside the
// database (R-UPD-2). It is built only by the parts that need it.
func ReleaseStore() do.Provider[*update.Store] {
	return func(i do.Injector) (*update.Store, error) {
		dir, _ := update.DirFor(do.MustInvoke[Bootstrap](i).DB)
		return update.Open(dir, update.WithLogger(do.MustInvoke[*slog.Logger](i)))
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
