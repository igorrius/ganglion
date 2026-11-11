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
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/pending"
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
// tools, its known-facts block and its nightly audit, or none of them when
// memory is off.
type Remembering struct {
	Tools        []tool.Handler
	Facts        agent.FactsFunc
	Jobs         map[string]agent.SystemJob
	QuickActions map[string]pending.QuickAction // the memory notes' Undo, Remember and Skip (R-MEM-9)
	View         agent.MemoryView               // what /memory reads
	Review       agent.Review                   // what the memory review stores through (R-MEM-8)
	On           bool                           // memory is on: git was found
}

// memoryReview is the memory and its notes as the review takes them.
type memoryReview struct {
	*memory.Memory
	builtin.MemoryNotes
}

// reviewOn reports whether the memory review runs: memory is on and
// memory.review.enabled (R-MEM-8).
func reviewOn(i do.Injector) bool {
	return do.MustInvoke[Remembering](i).On && do.MustInvoke[Bootstrap](i).Agent.Memory.Review
}

// ReviewDue provides when each conversation's memory review is due.
func ReviewDue() do.Provider[*agent.ReviewDue] {
	return func(i do.Injector) (*agent.ReviewDue, error) {
		return agent.NewReviewDue(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
	}
}

// reviewEvery is how often the review timer looks for due reviews.
const reviewEvery = 30 * time.Second

// ReviewTimer provides the component that puts the due memory reviews in
// the inbox.
func ReviewTimer() do.Provider[*agent.ReviewTimer] {
	return func(i do.Injector) (*agent.ReviewTimer, error) {
		return agent.NewReviewTimer(do.MustInvoke[*agent.ReviewDue](i), do.MustInvoke[*sqlqueue.Queue](i).Put, time.Now, reviewEvery,
			do.MustInvoke[*slog.Logger](i)), nil
	}
}

// Remember provides the memory's tools, the persona tool of the first-run
// introduction, the known-facts block within memory.prompt_budget and the
// code of its scheduled audit, moves facts of the one-fact shape into
// topics (ADR-0071), and makes sure the audit's system job exists. A host without git runs without memory: the
// tools are not registered, the prompt has no facts, and a warning says
// why (ADR-0033). Any other failure stops the start.
func Remember() do.Provider[Remembering] {
	return func(i do.Injector) (Remembering, error) {
		m, err := do.Invoke[*memory.Memory](i)
		if errors.Is(err, gitrepo.ErrNoGit) {
			do.MustInvoke[*slog.Logger](i).Warn("config: memory is off", "err", err)
			return Remembering{Facts: agent.NoFacts, Jobs: map[string]agent.SystemJob{}, QuickActions: map[string]pending.QuickAction{},
				View: agent.NoMemory{}, Review: agent.NoReview{}}, nil
		}
		if err != nil {
			return Remembering{}, err
		}
		if err := m.Migrate(do.MustInvoke[Startup](i)); err != nil {
			return Remembering{}, err
		}
		err = builtin.EnsureAudit(do.MustInvoke[Startup](i), do.MustInvoke[*schedule.Store](i), do.MustInvoke[alert.Admins](i),
			time.Now(), do.MustInvoke[*slog.Logger](i))
		b := do.MustInvoke[Bootstrap](i)
		notes := builtin.NewMemoryNotes(m, do.MustInvoke[*pending.Store](i), time.Now, b.Agent.TZ)
		tools := append(builtin.MemoryTools(m, telegram.PrivateConv, notes), builtin.NewPersonaPropose(m, builtin.WithPersonaLogger(do.MustInvoke[*slog.Logger](i))))
		jobs := map[string]agent.SystemJob{builtin.AuditJobID: builtin.NewAuditJob(m).Run}
		budget := b.Agent.Memory.PromptBudget
		return Remembering{Tools: tools, Facts: func() string { return m.Block(budget) }, Jobs: jobs, QuickActions: notes.QuickActions(),
			View: m, Review: memoryReview{m, notes}, On: true}, err
	}
}
