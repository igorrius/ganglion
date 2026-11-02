package config

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// Handler provides the turn handler: the agent when a model catalogue is
// set, the M2 echo bot otherwise.
func Handler() do.Provider[core.Handler] {
	return func(i do.Injector) (core.Handler, error) {
		var err error
		b, logger := need[Bootstrap](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		if b.Agent.Catalog == nil {
			logger.Warn("node: no GANGLION_MODELS_FILE; the agent is off and the bot echoes")
			return core.EchoHandler, nil
		}
		ag, err := do.Invoke[*agent.Agent](i)
		if err != nil {
			return nil, err
		}
		return ag.Handle, nil
	}
}

// GiveUp is what the turn loop sends for an event that failed every
// attempt.
type GiveUp func(e event.Event) []event.Outgoing

// GiveUpReplies provides the give-up replies: with the agent on, an
// apology to the person and an alert to the admins; the echo bot gives up
// silently.
func GiveUpReplies() do.Provider[GiveUp] {
	return func(i do.Injector) (GiveUp, error) {
		var err error
		b, admins, logger := need[Bootstrap](i, &err), need[alert.Admins](i, &err), need[*slog.Logger](i, &err)
		if err != nil || b.Agent.Catalog == nil {
			return func(event.Event) []event.Outgoing { return nil }, err
		}
		return func(e event.Event) []event.Outgoing {
			out := []event.Outgoing{{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv,
				Text: "Sorry, something went wrong with that message."}}
			alerts, err := admins.Messages(context.Background(), "alert:give-up:"+e.ID,
				"Gave up on event "+e.ID+" in "+e.Conv+" after "+strconv.Itoa(core.DefaultMaxAttempts)+" attempts.")
			if err != nil {
				logger.Warn("node: give-up alert not built", "id", e.ID, "err", err)
			}
			return append(out, alerts...)
		}, nil
	}
}

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
		cfg.DailyTurns, cfg.DailyTokens, cfg.SessionIdle, cfg.Location = b.Agent.DailyTurns, b.Agent.DailyTokens, b.Agent.SessionIdle, location(b)
		logger.Info("node: agent on", "providers", len(providers), "models", len(b.Agent.Catalog.Models))
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
		return rules.New(b.Agent.Catalog.RouterModels(), rules.WithBudget(tl, b.Agent.DailyBudgetUSD, location(b)))
	}
}

// providerFactories build a model provider of each catalogue type from
// its definition and its API key.
var providerFactories = map[string]func(p rules.ProviderDef, key string) provider.Provider{
	rules.TypeOpenAI: func(p rules.ProviderDef, key string) provider.Provider {
		return openai.New(p.Name, p.BaseURL, key)
	},
	rules.TypeClaudeCLI: func(p rules.ProviderDef, _ string) provider.Provider {
		var opts []claudecli.Option
		if p.Command != "" {
			opts = append(opts, claudecli.WithCommand(p.Command))
		}
		return claudecli.New(p.Name, opts...)
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

func location(b Bootstrap) *time.Location {
	if b.Agent.TZ == nil {
		return time.UTC
	}
	return b.Agent.TZ
}
