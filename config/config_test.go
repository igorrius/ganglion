package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

// Every combination of the optional parts builds, and shutting the
// container down releases the database: a wiring mistake fails here, not
// at start on a host.
func TestCoreContainerBuildsAndShutsDown(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	catalog, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: or, type: openai, base_url: " + model.URL() +
		", key_env: OR_KEY}\n  - {name: cli, type: claude-cli}\nmodels:\n  - {provider: or, name: m1, tier: smart, context: 64000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		token, admin string
		agent        bool
		components   int
	}{
		"echo, no transport":  {components: 3},
		"echo over Telegram":  {token: testToken, admin: "telegram:user:42", components: 4},
		"agent over Telegram": {token: testToken, admin: "telegram:user:42", agent: true, components: 9},
		"agent, no transport": {agent: true, components: 7},
	} {
		t.Run(name, func(t *testing.T) {
			b := testConfig(t)
			b.BotToken, b.BotAPIURL, b.Admin = tc.token, fake.URL(), tc.admin
			if tc.agent {
				b.Agent = AgentSettings{Catalog: &catalog, Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC, Workspace: t.TempDir()}
			}
			for range 2 { // the second build proves the first released the lock
				i := New(context.Background(), b, slog.New(slog.DiscardHandler))
				comps, err := do.Invoke[[]node.Component](i)
				if err != nil {
					t.Fatal(err)
				}
				if len(comps) != tc.components {
					t.Fatalf("%d components, want %d", len(comps), tc.components)
				}
				if _, err := do.Invoke[core.Handler](i); err != nil {
					t.Fatal(err)
				}
				if rep := i.Shutdown(); !rep.Succeed {
					t.Fatalf("shutdown: %v", rep.Error())
				}
			}
		})
	}
}

// modelConfig is agentConfig with its models served by model.
func modelConfig(t *testing.T, model *providertest.Server) Bootstrap {
	t.Helper()
	catalog, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: or, type: openai, base_url: " + model.URL() +
		", key_env: OR_KEY}\nmodels:\n  - {provider: or, name: m1, tier: smart, tools: true, context: 64000}\n" +
		"  - {provider: or, name: m2, tier: cheap, tools: true, context: 64000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := agentConfig(t)
	b.Agent.Catalog = &catalog
	return b
}

// The core role registers every tool of M3b, each for admins only, and
// runs the scheduler, the expiry of confirmations and the daily journal
// prune as components the node owns.
func TestCoreContainerBuildsWithTools(t *testing.T) {
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	reg, err := do.Invoke[*tool.Registry](i)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cells.get", "cells.set", "message.send", "plan.show", "plan.step", "schedule.create", "schedule.delete", "schedule.list",
		"schedule.run_now", "web.fetch", "web.search", "logs.search", "journal.search", "turns.failed", "skill.read"}
	if _, err := exec.LookPath("git"); err == nil {
		want = append(want, "memory.audit", "memory.forget", "memory.read", "memory.recall", "memory.write", "persona.propose")
	}
	for _, name := range want {
		s, ok := reg.Spec(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if !s.AdminOnly {
			t.Errorf("%s is offered to everyone", name)
		}
	}
	if got := len(reg.Specs(true)); got != len(want) {
		t.Errorf("%d tools on offer to an admin, want %d", got, len(want))
	}
	comps, err := do.Invoke[[]node.Component](i)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name())
	}
	for _, name := range []string{"sender", "loop", "prune", "prune-journal", "expire-pending", "scheduler"} {
		if !slices.Contains(names, name) {
			t.Errorf("component %s is missing from %v", name, names)
		}
	}
}

// The policy the core wires allows a call one level deep (a scheduled
// task's turn) and parks a confirmation with a real asker, not a nil one.
func TestPolicyHasDepthAndAsker(t *testing.T) {
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	p, err := do.Invoke[*policy.Policy](i)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	run := p.Middleware()(func(context.Context, tool.Call) (tool.Result, error) { ran++; return tool.Result{Text: "ran"}, nil })
	c := tool.Call{TurnID: "01EV1", N: 1, Conv: "telegram:42", Transport: "telegram", Principal: "telegram:user:42", Admin: true,
		Depth: 1, Name: "cells.get", Args: json.RawMessage(`{"id":"load.boiler"}`)}
	if res, err := run(context.Background(), c); err != nil || res.Text != "ran" || ran != 1 {
		t.Fatalf("a depth-1 read: %+v, %v", res, err)
	}
	c.Depth, c.N, c.Name, c.Args = 0, 2, "cells.set", json.RawMessage(`{"id":"load.boiler","value":"on"}`)
	res, err := run(context.Background(), c)
	if err != nil || res.Pending == "" || len(res.Out) != 1 || ran != 1 {
		t.Fatalf("a side effect was not parked for the owner: %+v, %v", res, err)
	}
}

// The journal the core wires labels its metrics with the registry's tool
// names; a made-up name stays "unknown".
func TestJournalKnowsTheRegistry(t *testing.T) {
	rec := telemetrytest.Install(t)
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	j, err := do.Invoke[*journal.Journal](i)
	if err != nil {
		t.Fatal(err)
	}
	run := j.Middleware()(func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, nil })
	_, _ = run(context.Background(), tool.Call{TurnID: "01EV1", N: 1, Name: "cells.get"})
	_, _ = run(context.Background(), tool.Call{TurnID: "01EV1", N: 2, Name: "made.up"})
	if got := rec.Value(t, "ganglion.tool.calls", map[string]string{"tool": "cells.get"}); got != 1 {
		t.Errorf("cells.get counted %v times, want 1", got)
	}
	if got := rec.Value(t, "ganglion.tool.calls", map[string]string{"tool": "unknown"}); got != 1 {
		t.Errorf("made.up counted as unknown %v times, want 1", got)
	}
}

// A family member gets plain conversation: no tools offered, no
// introduction, no memory; a tool call the model makes up anyway is
// refused by the policy and never runs (design section 4, rule 8).
func TestFamilyHasNoTools(t *testing.T) {
	model := providertest.NewServer(t, "sk-test")
	model.CallTool("cells.get", `{"id":"load.boiler"}`)
	model.Reply("I cannot do that here.")
	i := New(context.Background(), modelConfig(t, model), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	ctx := context.Background()
	if err := do.MustInvoke[*access.List](i).Allow(ctx, "telegram:user:7", access.RoleFamily, "Olena"); err != nil {
		t.Fatal(err)
	}
	if err := do.MustInvoke[*cells.Service](i).Define(ctx, cells.Def{ID: "load.boiler", Kind: cells.KindSwitch}); err != nil {
		t.Fatal(err)
	}
	if reg := do.MustInvoke[*tool.Registry](i); len(reg.Specs(false)) != 0 {
		t.Fatalf("tools for non-admins: %+v", reg.Specs(false))
	}
	p, _ := json.Marshal(event.Message{Text: "is the boiler on?", From: "telegram:user:7", Name: "Olena"})
	ev := event.Event{ID: "01EVFAMILY", IdemKey: "telegram:7001:1", Source: "telegram", Conv: "telegram:7", Kind: "message",
		Payload: p, At: time.Now()}
	out, _, err := do.MustInvoke[*agent.Agent](i).Handle(ctx, ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Text != "I cannot do that here." {
		t.Fatalf("replies %+v", out)
	}
	reqs := model.Requests()
	if len(reqs) != 2 || len(reqs[0].Tools) != 0 {
		t.Fatalf("%d requests, the first offering %d tools", len(reqs), len(reqs[0].Tools))
	}
	if sys, _ := reqs[0].Messages[0]["content"].(string); sys != agent.CoreRules+"\n\n"+agent.DefaultPersona {
		t.Errorf("a family member's system prompt %q", sys)
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if text, _ := last["content"].(string); last["role"] != "tool" || !strings.Contains(text, "not allowed") || strings.Contains(text, "in_sync") {
		t.Errorf("the model saw %v, want a refusal", last)
	}
	entries, err := do.MustInvoke[*journal.Journal](i).Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Decision != journal.DecisionForbidden || entries[0].Principal != "telegram:user:7" {
		t.Fatalf("journal %+v", entries)
	}
}

// The schedule tools read the registry when a call is checked, long after
// the container was built. The make accept run of the live-run fixes
// panicked there with a false circular dependency once Telegram's command
// hints made the transport build the agent: the lookup used the build-time
// scope, whose invoker chain was stale. It reads from the root scope now.
func TestScheduleSpecsLookupAfterTheBuild(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	b := agentConfig(t)
	b.BotToken, b.BotAPIURL = testToken, fake.URL()
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	if _, err := do.Invoke[[]node.Component](i); err != nil {
		t.Fatal(err)
	}
	sc, err := do.Invoke[builtin.Scheduling](i)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := sc.Specs("web.fetch"); !ok || s.Name != "web.fetch" {
		t.Fatalf("Specs(web.fetch) = %+v, %v", s, ok)
	}
}
