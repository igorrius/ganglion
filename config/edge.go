package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/edge"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// edgeCredsFile is the edge's .creds file beside the database when
// edge.credentials names none.
const edgeCredsFile = "edge.creds"

// EdgeOnly reports whether roles make the node an edge (R-EDGE-7): the edge
// role without the core. An edge with the core in one process shares the
// core's bus and has no link of its own.
func EdgeOnly(roles []node.Role) bool {
	return slices.Contains(roles, node.Edge) && !slices.Contains(roles, node.Core)
}

// edgeLeaf is the leaf setting of an edge's bus: its name as the domain and
// the link from the settings. A link without its credentials file stops the
// start: the edge would dial the core and be refused for ever.
func edgeLeaf(b Bootstrap) (*natsbus.Leaf, error) {
	e := b.Settings.Edge
	if e.Name == "" {
		return nil, fmt.Errorf("%s: edge.name must be set for --roles edge", settings.FileName)
	}
	l := &natsbus.Leaf{Domain: e.Name, URL: e.URL}
	if e.URL == "" {
		return l, nil
	}
	l.CredsFile = e.Credentials
	if l.CredsFile == "" {
		l.CredsFile = filepath.Join(filepath.Dir(b.DB), edgeCredsFile)
	}
	if _, err := os.Stat(l.CredsFile); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: edge.url is set but the edge's credentials file %s is missing; the core gives it when the edge joins", settings.FileName, l.CredsFile)
	} else if err != nil {
		return nil, fmt.Errorf("%s: edge.credentials: %w", settings.FileName, err)
	}
	return l, nil
}

// EdgeHandler provides an edge's turn handler: module cron jobs only.
func EdgeHandler() do.Provider[core.Handler] {
	return func(i do.Injector) (core.Handler, error) {
		return edge.CronHandler(do.MustInvoke[*schedule.Store](i), do.MustInvoke[*modhost.Host](i).RunCron, do.MustInvoke[*slog.Logger](i)), nil
	}
}

// EdgeComponents provides the edge role's components (R-EDGE-7): the
// module host with its cron jobs, the turn loop that runs them, and the
// telemetry. No Telegram, agent or model is built.
func EdgeComponents() do.Provider[[]node.Component] {
	return func(i do.Injector) ([]node.Component, error) {
		b, log := do.MustInvoke[Bootstrap](i), do.MustInvoke[*slog.Logger](i)
		nb := do.MustInvoke[*natsbus.Bus](i)
		host := do.MustInvoke[*modhost.Host](i)
		q := do.MustInvoke[*sqlqueue.Queue](i)
		every := time.Duration(b.Settings.Edge.TelemetryEvery)
		if every <= 0 { // a bootstrap made in code
			every = settings.DefaultTelemetryEvery
		}
		pub := &edge.Publisher{
			Edge:  b.Settings.Edge.Name,
			Every: every,
			Health: func(ctx context.Context) edge.Health {
				h := nb.EdgeHealth(ctx)
				return edge.Health{Linked: h.Linked, Events: h.Events}
			},
			Modules: func(ctx context.Context) ([]edge.Module, error) { return edgeModules(ctx, host) },
			Publish: nb.PublishTelemetry,
			Now:     time.Now,
			Log:     log,
		}
		if b.Telemetry != nil {
			pub.Metrics = b.Telemetry.Snapshot
		}
		log.Info("node: edge on", "edge", pub.Edge, "linked_to", b.Settings.Edge.URL != "", "telemetry_every", pub.Every)
		return []node.Component{
			node.Named("loop", do.MustInvoke[*core.Loop](i).Run),
			sqlqueue.Pruner{Queue: q, Every: pruneEvery, Log: log},
			do.MustInvoke[*schedule.Scheduler](i),
			pub,
		}, nil
	}
}

// edgeModules are the modules an edge runs, for its telemetry.
func edgeModules(ctx context.Context, h *modhost.Host) ([]edge.Module, error) {
	local, err := h.Local(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]edge.Module, 0, len(local))
	for _, m := range local {
		out = append(out, edge.Module{Name: m.Name, Version: m.Version})
	}
	return out, nil
}

// EdgeCollector provides the core's collector of its edges' telemetry: the
// edges' series on /metrics, their events copied again when they are heard
// after a silence, and a /status line per edge (ADR-0114).
func EdgeCollector() do.Provider[*edge.Collector] {
	return func(i do.Injector) (*edge.Collector, error) {
		b, nb := do.MustInvoke[Bootstrap](i), do.MustInvoke[*natsbus.Bus](i)
		c := &edge.Collector{
			Watch:  nb.WatchTelemetry,
			Resync: nb.ResyncEdge,
			Lags: func(ctx context.Context) ([]edge.Lag, error) {
				src, err := nb.EdgeSources(ctx)
				out := make([]edge.Lag, 0, len(src))
				for _, s := range src {
					out = append(out, edge.Lag{Edge: s.Edge, Waiting: s.Lag})
				}
				return out, err
			},
			Now: time.Now,
			Log: do.MustInvoke[*slog.Logger](i),
		}
		if b.Telemetry != nil {
			c.Metrics = b.Telemetry.Network()
		}
		return c, nil
	}
}
