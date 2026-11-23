//go:build lab

package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/edge"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// LabOptions are what chatlab changes in a node (ADR-0048). Nothing here
// reaches the ganglion binary: TestLabStaysOutOfTheBinary holds that.
type LabOptions struct {
	Models     settings.Models
	Keys       map[string]string
	Relay      string // the ganglion binary the claude CLI starts as its MCP server
	Resolver   web.Resolver
	Connect    web.ConnectFunc
	PendingNow func() time.Time // the confirmations' clock
	SchedNow   func() time.Time // the scheduler's clock, which the agent reads too
	// Wrap goes around every provider, to count tokens; nil leaves them as
	// they are.
	Wrap func(provider.Provider) provider.Provider
	// Modules are installed, as the owner's admin's, before the node runs:
	// a module a scenario needs without building it (a build is slow and
	// needs a toolchain).
	Modules []LabModule
	// MCPHTTP is the client of the remote MCP servers and their sign-ins:
	// one that reaches the lab's fakes; nil leaves the node's.
	MCPHTTP *http.Client
	// EdgeEntry opens the node's WebSocket entry for edges on a loopback
	// port, as the public listener would reach it, so a lab edge can link
	// to this node (LabNode.EdgeEntry).
	EdgeEntry bool
}

// LabModule is a built module a lab node has installed from the start.
type LabModule struct {
	Name     string // the short name, as in local/<name>
	Wasm     []byte
	Manifest []byte
}

// LabNode is a built container: what chatlab runs and looks at, so only this
// package wires the node (ADR-0028).
type LabNode struct {
	DB         *sql.DB
	Journal    *journal.Journal
	Components []node.Component
	Tick       func(context.Context) error // one tick of the scheduler and of the memory review's timer
	// Backlog is how many notices of the modules wait on the bus to be
	// delivered, which a lab counts as work the node has left.
	Backlog func(context.Context) (int, error)
	// Allow lists a family member by principal and name, so a scenario can have
	// another person in the owner's chat.
	Allow func(ctx context.Context, principal, name string) error
	// JoinEdge is an edge joining with a one-time code, as the join command
	// would on the owner's computer, with a key of its own made here. It
	// returns the edge's name and its credentials file.
	JoinEdge func(ctx context.Context, code string) (LabEdgeJoin, error)
	// EdgeEntry is the host:port of the node's WebSocket entry for edges, ""
	// unless LabOptions.EdgeEntry opened it.
	EdgeEntry func() string
	// InstallModule installs a build of a module as the owner's admin's, as
	// an update of the one installed before, and has the node run it or
	// deliver it to the edge its line names.
	InstallModule func(ctx context.Context, m LabModule) error
	// EdgeEvents is how many events of an edge the node's stream holds.
	EdgeEvents func(ctx context.Context, edgeName string) (uint64, error)
	// EdgeActual is what an edge says it runs of a module, as the node
	// reads it; false when it has said nothing.
	EdgeActual func(ctx context.Context, edgeName, module string) (edge.Actual, bool, error)
	Shutdown   func(context.Context)
}

// LabEdgeJoin is what an edge's join gave: its name and its credentials file.
type LabEdgeJoin struct {
	Name  string
	Creds []byte
}

// NewLab builds the core container of b with o's differences. The tables
// exist when it returns.
func NewLab(ctx context.Context, b Bootstrap, logger *slog.Logger, o LabOptions) (*LabNode, error) {
	i := New(ctx, b, logger)
	applyLab(i, o)
	if o.EdgeEntry {
		do.Override(i, busWith(func(c *natsbus.Config) { c.WebSocket = c.Chain != nil }))
	}
	components, err := do.Invoke[[]node.Component](i)
	if err != nil {
		return nil, err
	}
	if err := installLabModules(ctx, i, b.Admin, o.Modules); err != nil {
		return nil, err
	}
	scheduler := do.MustInvoke[*schedule.Scheduler](i)
	tick := scheduler.Tick
	if reviewOn(i) {
		reviews := do.MustInvoke[*agent.ReviewTimer](i)
		tick = func(ctx context.Context) error { return errors.Join(scheduler.Tick(ctx), reviews.Tick(ctx)) }
	}
	return &LabNode{DB: do.MustInvoke[Database](i).DB, Journal: do.MustInvoke[*journal.Journal](i), Components: components,
		Tick: tick, Backlog: do.MustInvoke[*natsbus.Consumer](i).Pending,
		Allow: func(ctx context.Context, principal, name string) error {
			return do.MustInvoke[*access.List](i).Allow(ctx, principal, access.RoleFamily, name)
		},
		JoinEdge: func(ctx context.Context, code string) (LabEdgeJoin, error) {
			svc := do.MustInvoke[*edgejoin.Service](i)
			if svc == nil {
				return LabEdgeJoin{}, errors.New("lab: the node takes no edges")
			}
			k, err := natsbus.NewUserKey()
			if err != nil {
				return LabEdgeJoin{}, err
			}
			pub, err := k.PublicKey()
			if err != nil {
				return LabEdgeJoin{}, err
			}
			ans, err := svc.Join(ctx, code, pub)
			if err != nil {
				return LabEdgeJoin{}, err
			}
			creds, err := k.Creds(ans.JWT)
			return LabEdgeJoin{Name: ans.Name, Creds: creds}, err
		},
		EdgeEntry: do.MustInvoke[*natsbus.Bus](i).WebSocketAddr,
		InstallModule: func(ctx context.Context, m LabModule) error {
			return installLabModules(ctx, i, b.Admin, []LabModule{m})
		},
		EdgeEvents: do.MustInvoke[*natsbus.Bus](i).EventsOf,
		EdgeActual: func(ctx context.Context, edgeName, module string) (edge.Actual, bool, error) {
			raw, ok, err := do.MustInvoke[*natsbus.Bus](i).Actual(ctx, edgeName, module)
			if err != nil || !ok {
				return edge.Actual{}, false, err
			}
			var a edge.Actual
			return a, true, json.Unmarshal(raw, &a)
		},
		Shutdown: func(ctx context.Context) { _ = i.ShutdownWithContext(ctx) },
	}, nil
}

// installLabModules installs each of mods as a local module by admin, so its
// tools, cells and cron job exist when the node starts.
func installLabModules(ctx context.Context, i do.Injector, admin string, mods []LabModule) error {
	for _, m := range mods {
		b := modstore.Build{Module: modstore.LocalPrefix + m.Name, Wasm: m.Wasm, Manifest: m.Manifest, Language: "go", By: admin}
		o, err := do.MustInvoke[*modstore.Store](i).InstallLocal(ctx, b, modstore.Digest(b.Wasm, b.Manifest))
		if err != nil || o.Result != modstore.ResultInstalled {
			return fmt.Errorf("lab: install module %s: %v %v", m.Name, o.Result, err)
		}
		if err := do.MustInvoke[*modhost.Host](i).Reload(ctx, b.Module); err != nil {
			return fmt.Errorf("lab: start module %s: %w", m.Name, err)
		}
	}
	return nil
}

// applyLabHousekeeping gives the lab its housekeepers: the journal's prune,
// an expirer that looks every 250 ms, the runner of host commands, which
// waits labCommandWait and announces every labPass, the build queue and the
// client of remote MCP servers. The janitor stays out,
// since it would clean the developer's own temporary folders, and so does the
// translator, as before.
func applyLabHousekeeping(i do.Injector) {
	do.Override(i, func(i do.Injector) (*host.Runner, error) {
		lim := host.DefaultLimits()
		lim.Wait = labCommandWait
		return newRunner(i, lim, host.WithRunnerClock(time.Now, labRunnerAfter(lim.Wait)))
	})
	do.Override(i, func(i do.Injector) (Housekeeping, error) {
		logger := do.MustInvoke[*slog.Logger](i)
		fast := func(time.Duration) <-chan time.Time { return time.After(250 * time.Millisecond) }
		return Housekeeping{
			journal.Pruner{Journal: do.MustInvoke[*journal.Journal](i), Every: pruneEvery, Log: logger},
			pending.NewExpirer(do.MustInvoke[*pending.Store](i), do.MustInvoke[*sqlqueue.Queue](i).Put, logger, pending.WithAfter(fast)),
			do.MustInvoke[*host.Runner](i),
			do.MustInvoke[*natsbus.Consumer](i), // the modules' notices go through the stream here too
			do.MustInvoke[*builtin.BuildQueue](i),
			node.Named("mcpclient", do.MustInvoke[*mcpclient.Hub](i).Run),
		}, nil
	})
}

// labCommandWait is how long a lab command call waits before its command goes
// on in the background: short, so a scenario sees a background command
// within its patience.
const labCommandWait = 5 * time.Second

// labPass is how often the lab's runner announces the background commands
// that ended: soon, so their command.done turn follows within a step.
const labPass = 250 * time.Millisecond

// labRunnerAfter keeps a command call's wait and shortens the runner's other
// wait, the pass between announcements, to labPass.
func labRunnerAfter(wait time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		if d != wait {
			d = labPass
		}
		return time.After(d)
	}
}

// applyLab replaces in i what differs from a node started by `ganglion run`,
// and nothing else: the providers, the fetcher's way to the test pages, the
// confirmations' clock, the housekeepers (applyLabHousekeeping) and a
// scheduler, with the agent's clock, that ticks only when told to.
func applyLab(i do.Injector, o LabOptions) {
	do.Override(i, func(i do.Injector) (map[string]provider.Provider, error) {
		claudeDir := do.MustInvoke[Bootstrap](i).ClaudeDir
		out := map[string]provider.Provider{}
		for _, p := range o.Models.Providers {
			var built provider.Provider
			if p.Type == settings.TypeClaudeCLI {
				built = claudecli.New(p.Name, claudeOptions(claudeDir, claudecli.WithCommand(p.Command), claudecli.WithRelay(o.Relay))...)
			} else {
				built = providerFactories[p.Type](p, o.Keys[p.Name], claudeDir)
			}
			if o.Wrap != nil {
				built = o.Wrap(built)
			}
			out[p.Name] = built
		}
		return out, nil
	})
	do.Override(i, func(i do.Injector) (*web.Fetcher, error) {
		return web.NewFetcher(web.DefaultFetchConfig(), web.NewModuleConverter(do.MustInvoke[*runner.SwapTool](i)),
			web.WithResolver(o.Resolver), web.WithConnect(o.Connect)), nil
	})
	do.Override(i, func(i do.Injector) (*pending.Store, error) {
		return pending.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*cells.Service](i), o.PendingNow, pending.DefaultTTL)
	})
	applyLabHousekeeping(i)
	applyLabMCP(i, o)
	// The model reads the time the scheduler goes by, or a reminder looks as
	// if it fired early.
	do.Override(i, func(i do.Injector) (*agent.Agent, error) {
		cfg, deps := agentParts(i)
		deps.Now = o.SchedNow
		return agent.New(cfg, deps), nil
	})
	// The memory review's timer goes by the same clock and ticks only with
	// the scheduler; the lab's housekeepers leave it out.
	do.Override(i, func(i do.Injector) (*agent.ReviewTimer, error) {
		return agent.NewReviewTimer(do.MustInvoke[*agent.ReviewDue](i), do.MustInvoke[*sqlqueue.Queue](i).Put, o.SchedNow, time.Hour,
			do.MustInvoke[*slog.Logger](i)), nil
	})
	do.Override(i, func(i do.Injector) (*schedule.Scheduler, error) {
		never := func(time.Duration) <-chan time.Time { return nil }
		return schedule.New(do.MustInvoke[*schedule.Store](i), do.MustInvoke[*sqlqueue.Queue](i), do.MustInvoke[Bootstrap](i).Agent.TZ,
			o.SchedNow, schedule.WithAfter(never), schedule.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	})
}

// applyLabMCP gives the node the lab's way to its fake MCP servers, and an
// intake page that needs no network: the hosted page is this release's
// copy.
func applyLabMCP(i do.Injector, o LabOptions) {
	if o.MCPHTTP != nil {
		do.Override(i, func(do.Injector) (MCPHTTPClient, error) { return MCPHTTPClient{o.MCPHTTP}, nil })
	}
	do.Override(i, func(do.Injector) (intake.PageCheckFunc, error) {
		return func(context.Context) error { return nil }, nil
	})
}
