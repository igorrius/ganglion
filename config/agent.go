package config

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/diag"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/host"
	bgjobs "github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// Handler provides the turn handler.
func Handler() do.Provider[core.Handler] {
	return choose[core.Handler](agentOn, handlerAgent, handlerEcho)
}

// AgentHandler provides the agent as the turn handler.
func AgentHandler() do.Provider[core.Handler] {
	return func(i do.Injector) (core.Handler, error) { return do.MustInvoke[*agent.Agent](i).Handle, nil }
}

// GiveUp provides the replies for an event that failed every attempt.
func GiveUp() do.Provider[core.GiveUp] { return choose[core.GiveUp](agentOn, giveUpAgent, giveUpNone) }

// AgentGiveUp provides the agent's apology and admin alert.
func AgentGiveUp() do.Provider[core.GiveUp] {
	return func(i do.Injector) (core.GiveUp, error) { return do.MustInvoke[*agent.Agent](i).GiveUp, nil }
}

// Agent provides the agent over the catalogue's models.
func Agent() do.Provider[*agent.Agent] {
	return func(i do.Injector) (*agent.Agent, error) {
		cfg, deps := agentParts(i)
		return agent.New(cfg, deps), nil
	}
}

// agentParts are the agent's settings and collaborators.
func agentParts(i do.Injector) (agent.Config, agent.Deps) {
	b := do.MustInvoke[Bootstrap](i)
	cfg := agent.DefaultConfig()
	cfg.DailyTurns, cfg.DailyTokens, cfg.SessionIdle, cfg.Location = b.Agent.DailyTurns, b.Agent.DailyTokens, b.Agent.SessionIdle, b.Agent.TZ
	cfg.CallTimeout, cfg.TurnTimeout = b.Timeouts.Call, b.Timeouts.Turn
	cfg.Home = commandHome()
	cfg.Review, cfg.ReviewQuiet = reviewOn(i), b.Agent.Memory.Quiet
	return cfg, agent.Deps{
		States:    do.MustInvoke[*sqlqueue.Queue](i),
		Languages: do.MustInvoke[*language.Store](i),
		Texts:     do.MustInvoke[*texts.Catalog](i),
		Translate: do.MustInvoke[*language.Translator](i).Need,
		Router:    do.MustInvoke[router.Router](i),
		Providers: do.MustInvoke[map[string]provider.Provider](i),
		Tools:     do.MustInvoke[*tool.Registry](i),
		// Around every tool call, outermost first: traced, journal, then
		// the policy for the calls the model makes.
		Middleware:    []tool.Middleware{do.MustInvoke[*journal.Journal](i).Middleware()},
		Policy:        do.MustInvoke[*policy.Policy](i).Middleware(),
		Pending:       do.MustInvoke[*pending.Store](i),
		Outbox:        do.MustInvoke[*sqlqueue.Queue](i),
		Inbox:         do.MustInvoke[*sqlqueue.Queue](i),
		Status:        do.MustInvoke[agent.StatusFunc](i),
		Plans:         do.MustInvoke[*plan.Store](i),
		Sent:          do.MustInvoke[*sqlqueue.Queue](i),
		QuickActions:  quickActions(i),
		Jobs:          do.MustInvoke[*schedule.Store](i),
		IsAdmin:       do.MustInvoke[agent.AdminFunc](i),
		Names:         do.MustInvoke[*access.List](i).Name,
		Turns:         do.MustInvoke[*turns.Log](i),
		Archive:       do.MustInvoke[agent.SessionStore](i),
		Alerts:        do.MustInvoke[alert.Admins](i),
		Persona:       agent.PersonaDir(b.Agent.Workspace),
		Facts:         do.MustInvoke[Remembering](i).Facts,
		Memory:        do.MustInvoke[Remembering](i).View,
		Private:       telegram.PrivateConv,
		Reviews:       reviews(i),
		Review:        do.MustInvoke[Remembering](i).Review,
		SystemJobs:    do.MustInvoke[Remembering](i).Jobs,
		Restart:       do.MustInvoke[agent.RestartFunc](i),
		Modules:       do.MustInvoke[agent.Modules](i),
		Local:         do.MustInvoke[*modhost.Host](i),
		Cells:         do.MustInvoke[*cells.Service](i),
		Buttons:       do.MustInvoke[*pending.Store](i),
		ModuleCron:    do.MustInvoke[*modhost.Host](i).RunCron,
		ModuleTrigger: do.MustInvoke[*modhost.Host](i).RunTrigger,
		Switch:        do.MustInvoke[*host.Switch](i),
		Commands:      do.MustInvoke[*host.Runner](i),
		Builds:        do.MustInvoke[*builtin.BuildQueue](i),
		Background:    do.MustInvoke[*bgjobs.Registry](i),
		Skills:        do.MustInvoke[Skilled](i).Set,
		StatusSources: do.MustInvoke[[]status.Source](i),
		Log:           do.MustInvoke[*slog.Logger](i),
	}
}

// Policy provides the tool policy: the default settings with the node user's
// home, alerts to the admins without the node's secrets, the node's calendar
// day.
func Policy() do.Provider[*policy.Policy] {
	return func(i do.Injector) (*policy.Policy, error) {
		b := do.MustInvoke[Bootstrap](i)
		cfg := policy.DefaultConfig()
		cfg.Home = commandHome()
		return policy.New(cfg, do.MustInvoke[*tool.Registry](i), do.MustInvoke[policy.Asker](i),
			policy.WithAlerts(do.MustInvoke[alert.Admins](i)),
			policy.WithRedact(secretRedact(b)),
			policy.WithClock(time.Now, b.Agent.TZ),
			policy.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// Asker provides the owner's confirmations: pending calls with buttons.
func Asker() do.Provider[policy.Asker] {
	return func(i do.Injector) (policy.Asker, error) { return do.MustInvoke[*pending.Store](i), nil }
}

// IsAdmin provides the agent's role check, backed by the access list.
func IsAdmin() do.Provider[agent.AdminFunc] {
	return func(i do.Injector) (agent.AdminFunc, error) {
		return do.MustInvoke[*access.List](i).IsAdmin, nil
	}
}

// Plans provides the conversations' plans.
func Plans() do.Provider[*plan.Store] {
	return func(i do.Injector) (*plan.Store, error) {
		return plan.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*sqlqueue.Queue](i), plan.WithHome(commandHome()))
	}
}

// Translator provides the background translation of fixed texts.
func Translator() do.Provider[*language.Translator] {
	return func(i do.Injector) (*language.Translator, error) {
		return language.NewTranslator(do.MustInvoke[*language.Store](i), do.MustInvoke[*texts.Catalog](i),
			do.MustInvoke[router.Router](i), do.MustInvoke[map[string]provider.Provider](i),
			language.WithTranslatorLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// Tools provides the tool registry. Each tool joins by one line in
// toolHandlers; the tools of the owner's modules come and go with them.
func Tools() do.Provider[*tool.Registry] {
	return func(i do.Injector) (*tool.Registry, error) {
		return tool.NewRegistryWith(router.Local{}, []tool.RegistryOption{
			tool.WithDynamic(do.MustInvoke[*modhost.Host](i).Handlers),
			tool.WithRegistryLogger(do.MustInvoke[*slog.Logger](i)),
		}, toolHandlers(i)...)
	}
}

// toolHandlers lists the tools the agent has; each tool joins by one line.
func toolHandlers(i do.Injector) []tool.Handler {
	cs := do.MustInvoke[*cells.Service](i)
	return append([]tool.Handler{
		builtin.NewMessageSend(do.MustInvoke[builtin.Convs](i)),
		builtin.NewCellsGet(cs, time.Now),
		builtin.NewCellsList(cs, time.Now),
		builtin.NewCellsSet(cs),
		builtin.NewScheduleCreate(do.MustInvoke[builtin.Scheduling](i)),
		builtin.NewScheduleList(do.MustInvoke[builtin.Scheduling](i)),
		builtin.NewScheduleDelete(do.MustInvoke[builtin.Scheduling](i)),
		builtin.NewScheduleRunNow(do.MustInvoke[builtin.Scheduling](i)),
		builtin.NewLanguageSet(do.MustInvoke[*language.Store](i)),
		builtin.NewHostStats(do.MustInvoke[*host.Stats](i)),
		do.MustInvoke[*builtin.HostExec](i),
	}, slices.Concat(do.MustInvoke[Remembering](i).Tools, do.MustInvoke[Reading](i).Tools,
		builtin.NewPlanTools(do.MustInvoke[*plan.Store](i), do.MustInvoke[*builtin.HostExec](i)),
		builtin.NewDiagTools(do.MustInvoke[*diag.Diag](i), telegram.PrivateConv), do.MustInvoke[Skilled](i).Tools,
		do.MustInvoke[Moduling](i).Tools, do.MustInvoke[builtin.Routing](i).Tools(), metricsTools(do.MustInvoke[Bootstrap](i)))...)
}

// metricsTools is metrics.show when the command line gave a renderer.
func metricsTools(b Bootstrap) []tool.Handler {
	if b.Metrics == nil {
		return nil
	}
	return []tool.Handler{builtin.NewMetricsShow(b.Metrics)}
}

// Skilled is the loaded skills and their tools.
type Skilled struct {
	Set      skills.Set
	Tools    []tool.Handler
	Problems []skills.Problem // the workspace skills not loaded, for /status
}

// Skills loads the built-in and the workspace skills; a script never
// gets a variable that holds one of the node's secrets (R-RTR-5,
// ADR-0042).
func Skills() do.Provider[Skilled] {
	return func(i do.Injector) (Skilled, error) {
		b := do.MustInvoke[Bootstrap](i)
		set, problems, err := skills.Load(skills.Builtin, b.Agent.Workspace, nodeSecrets(b))
		if err != nil {
			return Skilled{}, err
		}
		skills.Report(do.MustInvoke[*slog.Logger](i), set, problems)
		cfg := skills.DefaultRunConfig()
		cfg.TZ = b.Agent.TZ.String()
		runs, err := skills.OpenRuns(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now)
		if err != nil {
			return Skilled{}, err
		}
		box := do.MustInvoke[*sandbox.Sandbox](i)
		return Skilled{Set: set, Tools: builtin.NewSkillTools(set, skills.NewRunner(cfg, box), runs, sandboxGate{box}), Problems: problems}, nil
	}
}

// nodeSecrets are the node's own secrets: every env: reference of the
// file, the providers' keys of models set without a file (the lab), and
// the secret store's master key, sorted and each once. A skill script may not list them, and no module is
// granted one.
func nodeSecrets(b Bootstrap) []string {
	deny := append(b.Settings.SecretNames(), vault.KeyEnv)
	if m := b.Agent.Models; m != nil {
		deny = append(deny, m.KeyNames()...)
	}
	slices.Sort(deny)
	return slices.Compact(deny)
}

// Diagnostics provides the diagnostic searches over the node's own
// database and log files (R-OBS-7); config.show and queues.state stay
// with `ganglion mcp`, so no settings are passed.
func Diagnostics() do.Provider[*diag.Diag] {
	return func(i do.Injector) (*diag.Diag, error) {
		return diag.New(do.MustInvoke[Database](i).DB, do.MustInvoke[Bootstrap](i).LogDir, nil, time.Now), nil
	}
}

// Scheduler provides the scheduler of the jobs in the node's zone, which
// puts their events into the inbox.
func Scheduler() do.Provider[*schedule.Scheduler] {
	return func(i do.Injector) (*schedule.Scheduler, error) {
		return schedule.New(do.MustInvoke[*schedule.Store](i), do.MustInvoke[*sqlqueue.Queue](i), do.MustInvoke[Bootstrap](i).Agent.TZ,
			time.Now, schedule.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// Scheduling provides what the schedule tools and their buttons need.
// The tools check grants against the registry they are part of, so the
// spec lookup reads the registry when called, not when built, and from
// the root scope: the scope a provider gets carries the invoker chain of
// its build, which a later lookup would read as a circular dependency.
func Scheduling() do.Provider[builtin.Scheduling] {
	return func(i do.Injector) (builtin.Scheduling, error) {
		root := i.RootScope()
		return builtin.Scheduling{
			Jobs:     do.MustInvoke[*schedule.Store](i),
			Runner:   do.MustInvoke[*schedule.Scheduler](i),
			Specs:    func(name string) (tool.Spec, bool) { return do.MustInvoke[*tool.Registry](root).Spec(name) },
			Buttons:  do.MustInvoke[*pending.Store](i),
			Convs:    do.MustInvoke[builtin.Convs](i),
			Commands: do.MustInvoke[*builtin.HostExec](i),
			Loc:      do.MustInvoke[Bootstrap](i).Agent.TZ,
			Now:      time.Now,
		}, nil
	}
}

// Convs provides where message.send may deliver: conversations with a
// stored state and the private chats of principals on the access list.
func Convs() do.Provider[builtin.Convs] {
	return func(i do.Injector) (builtin.Convs, error) {
		return builtin.Convs{
			States:  do.MustInvoke[*sqlqueue.Queue](i),
			Listed:  do.MustInvoke[*access.List](i).Listed,
			Private: telegram.PrivateConv,
			Names:   do.MustInvoke[*access.List](i).Name,
			Dests:   do.MustInvoke[groups.Destinations](i).Resolve,
			Place:   do.MustInvoke[builtin.Routing](i).Place,
			Called:  do.MustInvoke[builtin.Routing](i).Called,
			Learn:   do.MustInvoke[builtin.Routing](i).Learn,
		}, nil
	}
}

// Router provides the rules router over the models and their purposes,
// with the daily budget from the turn trace.
func Router() do.Provider[router.Router] {
	return func(i do.Injector) (router.Router, error) {
		b, tl := do.MustInvoke[Bootstrap](i), do.MustInvoke[*turns.Log](i)
		return rules.New(b.Agent.Models.RouterModels(), b.Agent.Models.RouterPurposes(),
			rules.WithBudget(tl, b.Agent.DailyBudgetUSD, b.Agent.TZ))
	}
}

// claudeOptions adds the CLI's work folder to opts; an empty dir keeps the
// private temporary folder (a test node).
func claudeOptions(dir string, opts ...claudecli.Option) []claudecli.Option {
	if dir == "" {
		return opts
	}
	return append(opts, claudecli.WithWorkDir(dir))
}

// providerFactories build a model provider of each type from its
// definition and its API key.
var providerFactories = map[string]func(p settings.Provider, key, claudeDir string) provider.Provider{
	settings.TypeOpenAI: func(p settings.Provider, key, _ string) provider.Provider {
		return openai.New(p.Name, p.BaseURL, key)
	},
	settings.TypeClaudeCLI: func(p settings.Provider, _, claudeDir string) provider.Provider {
		return claudecli.New(p.Name, claudeOptions(claudeDir, claudecli.WithCommand(p.Command))...)
	},
}

// Providers provides the models section's providers by name.
func Providers() do.Provider[map[string]provider.Provider] {
	return func(i do.Injector) (map[string]provider.Provider, error) {
		b := do.MustInvoke[Bootstrap](i)
		out := map[string]provider.Provider{}
		for _, p := range b.Agent.Models.Providers {
			build, ok := providerFactories[p.Type]
			if !ok {
				return nil, fmt.Errorf("config: provider %s: unknown type %q", p.Name, p.Type)
			}
			out[p.Name] = build(p, b.Agent.Keys[p.Name], b.ClaudeDir)
		}
		return out, nil
	}
}

// quickActions are every quick-action button by action name: the
// scheduler's, the memory notes' and those under a group's request.
func quickActions(i do.Injector) map[string]pending.QuickAction {
	out := do.MustInvoke[builtin.Scheduling](i).QuickActions()
	maps.Copy(out, do.MustInvoke[Remembering](i).QuickActions)
	maps.Copy(out, do.MustInvoke[groups.Actions](i).QuickActions())
	return out
}

// reviews arms the memory review: the due table while the review runs,
// nothing otherwise.
func reviews(i do.Injector) agent.Reviews {
	if !reviewOn(i) {
		return agent.NoReviews{}
	}
	return do.MustInvoke[*agent.ReviewDue](i)
}
