package config

import (
	"errors"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/guard"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// Guard provides the clean-context check on the catalogue's cheap models.
func Guard() do.Provider[*guard.Guard] {
	return func(i do.Injector) (*guard.Guard, error) {
		return guard.New(do.MustInvoke[router.Router](i), do.MustInvoke[map[string]provider.Provider](i),
			guard.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// Workspace provides the agent's workspace, a git repository created at
// start when it is not one yet (ADR-0033): memory and the modules' sources
// live in it. gitrepo.ErrNoGit when the git binary is missing.
func Workspace() do.Provider[*gitrepo.Repo] {
	return func(i do.Injector) (*gitrepo.Repo, error) {
		return gitrepo.Open(do.MustInvoke[Startup](i), do.MustInvoke[Bootstrap](i).Agent.Workspace)
	}
}

// Memory provides the facts in the workspace's git repository; recall
// searches the session archive. gitrepo.ErrNoGit when the git binary is
// missing.
func Memory() do.Provider[*memory.Memory] {
	return func(i do.Injector) (*memory.Memory, error) {
		repo, err := do.Invoke[*gitrepo.Repo](i)
		if err != nil {
			return nil, err
		}
		return memory.New(repo, do.MustInvoke[*guard.Guard](i), time.Now,
			memory.WithAlerts(do.MustInvoke[alert.Admins](i)),
			memory.WithEpisodes(do.MustInvoke[agent.SessionStore](i)),
			memory.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// Remembering is the memory as the agent and its tools take it: its
// tools, its index and its nightly audit, or none of them when memory is
// off.
type Remembering struct {
	Tools []tool.Handler
	Facts agent.FactsFunc
	Jobs  map[string]agent.SystemJob
}

// Remember provides the memory's tools, the persona tool of the first-run
// introduction, the memory's index and the code of its scheduled audit,
// and makes sure the audit's system job exists. A host without git runs without memory: the
// tools are not registered, the prompt has no facts, and a warning says
// why (ADR-0033). Any other failure stops the start.
func Remember() do.Provider[Remembering] {
	return func(i do.Injector) (Remembering, error) {
		m, err := do.Invoke[*memory.Memory](i)
		if errors.Is(err, gitrepo.ErrNoGit) {
			do.MustInvoke[*slog.Logger](i).Warn("config: memory is off", "err", err)
			return Remembering{Facts: agent.NoFacts, Jobs: map[string]agent.SystemJob{}}, nil
		}
		if err != nil {
			return Remembering{}, err
		}
		err = builtin.EnsureAudit(do.MustInvoke[Startup](i), do.MustInvoke[*schedule.Store](i), do.MustInvoke[alert.Admins](i),
			time.Now(), do.MustInvoke[*slog.Logger](i))
		tools := append(builtin.MemoryTools(m, telegram.PrivateConv), builtin.NewPersonaPropose(m, builtin.WithPersonaLogger(do.MustInvoke[*slog.Logger](i))))
		jobs := map[string]agent.SystemJob{builtin.AuditJobID: builtin.NewAuditJob(m).Run}
		return Remembering{Tools: tools, Facts: m.Index, Jobs: jobs}, err
	}
}
