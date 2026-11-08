//go:build lab

package config

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// LabOptions are what chatlab changes in a node (ADR-0048). Nothing here
// reaches the ganglion binary: TestLabStaysOutOfTheBinary holds that.
type LabOptions struct {
	Catalog    rules.Catalog
	Keys       map[string]string
	Relay      string // the ganglion binary the claude CLI starts as its MCP server
	Resolver   web.Resolver
	Connect    web.ConnectFunc
	PendingNow func() time.Time // the confirmations' clock
	SchedNow   func() time.Time // the scheduler's clock, which the agent reads too
	// Wrap goes around every provider, to count tokens; nil leaves them as
	// they are.
	Wrap func(provider.Provider) provider.Provider
}

// LabNode is a built container: what chatlab runs and looks at, so only this
// package wires the node (ADR-0028).
type LabNode struct {
	DB         *sql.DB
	Journal    *journal.Journal
	Components []node.Component
	Tick       func(context.Context) error // one tick of the scheduler
	// Allow lists a family member by principal and name, so a scenario can have
	// another person in the owner's chat.
	Allow    func(ctx context.Context, principal, name string) error
	Shutdown func(context.Context)
}

// NewLab builds the core container of b with o's differences. The tables
// exist when it returns.
func NewLab(ctx context.Context, b Bootstrap, logger *slog.Logger, o LabOptions) (*LabNode, error) {
	i := New(ctx, b, logger)
	applyLab(i, o)
	components, err := do.Invoke[[]node.Component](i)
	if err != nil {
		return nil, err
	}
	scheduler := do.MustInvoke[*schedule.Scheduler](i)
	return &LabNode{DB: do.MustInvoke[Database](i).DB, Journal: do.MustInvoke[*journal.Journal](i), Components: components,
		Tick: scheduler.Tick,
		Allow: func(ctx context.Context, principal, name string) error {
			return do.MustInvoke[*access.List](i).Allow(ctx, principal, access.RoleFamily, name)
		},
		Shutdown: func(ctx context.Context) { _ = i.ShutdownWithContext(ctx) },
	}, nil
}

// applyLabHousekeeping gives the lab its housekeepers: the journal's prune,
// an expirer that looks every 250 ms, and the runner of host commands, which
// waits labCommandWait and announces every labPass. The janitor stays out,
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
		for _, p := range o.Catalog.Providers {
			var built provider.Provider
			if p.Type == rules.TypeClaudeCLI {
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
	// The model reads the time the scheduler goes by, or a reminder looks as
	// if it fired early.
	do.Override(i, func(i do.Injector) (*agent.Agent, error) {
		cfg, deps := agentParts(i)
		deps.Now = o.SchedNow
		return agent.New(cfg, deps), nil
	})
	do.Override(i, func(i do.Injector) (*schedule.Scheduler, error) {
		never := func(time.Duration) <-chan time.Time { return nil }
		return schedule.New(do.MustInvoke[*schedule.Store](i), do.MustInvoke[*sqlqueue.Queue](i), do.MustInvoke[Bootstrap](i).Agent.TZ,
			o.SchedNow, schedule.WithAfter(never), schedule.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	})
}
