package config

import (
	"log/slog"

	"fmt"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// Handler provides the turn handler.
func Handler() do.Provider[core.Handler] { return choose[core.Handler](agentOrEcho) }

// AgentHandler provides the agent as the turn handler.
func AgentHandler() do.Provider[core.Handler] {
	return func(i do.Injector) (core.Handler, error) {
		ag, err := do.Invoke[*agent.Agent](i)
		if err != nil {
			return nil, err
		}
		return ag.Handle, nil
	}
}

// EchoHandler provides the M2 echo bot as the turn handler.
func EchoHandler() do.Provider[core.Handler] { return value[core.Handler](core.EchoHandler) }

// GiveUp provides the replies for an event that failed every attempt.
func GiveUp() do.Provider[core.GiveUp] { return choose[core.GiveUp](agentOrEcho) }

// AgentGiveUp provides the agent's apology and admin alert.
func AgentGiveUp() do.Provider[core.GiveUp] {
	return func(i do.Injector) (core.GiveUp, error) {
		ag, err := do.Invoke[*agent.Agent](i)
		if err != nil {
			return nil, err
		}
		return ag.GiveUp, nil
	}
}

// NoGiveUp provides giving up silently.
func NoGiveUp() do.Provider[core.GiveUp] { return value[core.GiveUp](core.NoGiveUp) }

// Agent provides the agent over the catalogue's models.
func Agent() do.Provider[*agent.Agent] {
	return func(i do.Injector) (*agent.Agent, error) {
		var err error
		b, q, logger := need[Bootstrap](i, &err), need[*sqlqueue.Queue](i, &err), need[*slog.Logger](i, &err)
		rt, providers, tl := need[router.Router](i, &err), need[map[string]provider.Provider](i, &err), need[*turns.Log](i, &err)
		archive, admins := need[agent.SessionStore](i, &err), need[alert.Admins](i, &err)
		if err != nil {
			return nil, err
		}
		cfg := agent.DefaultConfig()
		cfg.DailyTurns, cfg.DailyTokens, cfg.SessionIdle, cfg.Location = b.Agent.DailyTurns, b.Agent.DailyTokens, b.Agent.SessionIdle, b.Agent.TZ
		return agent.New(cfg, agent.Deps{
			States: q, Router: rt, Providers: providers, Tools: agent.NoTools{}, Turns: tl, Archive: archive,
			Alerts: admins, Log: logger, Persona: agent.PersonaDir(b.Agent.Workspace),
		}), nil
	}
}

// Router provides the rules router over the catalogue, with the daily
// budget from the turn trace.
func Router() do.Provider[router.Router] {
	return func(i do.Injector) (router.Router, error) {
		var err error
		b, tl := need[Bootstrap](i, &err), need[*turns.Log](i, &err)
		if err != nil {
			return nil, err
		}
		return rules.New(b.Agent.Catalog.RouterModels(), rules.WithBudget(tl, b.Agent.DailyBudgetUSD, b.Agent.TZ))
	}
}

// providerFactories build a model provider of each catalogue type from
// its definition and its API key.
var providerFactories = map[string]func(p rules.ProviderDef, key string) provider.Provider{
	rules.TypeOpenAI: func(p rules.ProviderDef, key string) provider.Provider {
		return openai.New(p.Name, p.BaseURL, key)
	},
	rules.TypeClaudeCLI: func(p rules.ProviderDef, _ string) provider.Provider {
		return claudecli.New(p.Name, claudecli.WithCommand(p.Command))
	},
}

// Providers provides the catalogue's providers by name.
func Providers() do.Provider[map[string]provider.Provider] {
	return func(i do.Injector) (map[string]provider.Provider, error) {
		b, err := do.Invoke[Bootstrap](i)
		if err != nil {
			return nil, err
		}
		out := map[string]provider.Provider{}
		for _, p := range b.Agent.Catalog.Providers {
			build, ok := providerFactories[p.Type]
			if !ok {
				return nil, fmt.Errorf("config: provider %s: unknown type %q", p.Name, p.Type)
			}
			out[p.Name] = build(p, b.Agent.Keys[p.Name])
		}
		return out, nil
	}
}
