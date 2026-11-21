package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/settings"
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
	models := parseModels(t, "providers:\n  - {name: or, type: openai, base_url: \""+model.URL()+
		"\", key: env:OR_KEY}\n  - {name: cli, type: claude-cli}\nmodels:\n  - {provider: or, name: m1, context: 64000}\npurposes: {chat: [{model: m1}]}\n")
	for name, tc := range map[string]struct {
		token, admin string
		agent        bool
		components   int
	}{
		"echo, no transport":  {components: 4},
		"echo over Telegram":  {token: testToken, admin: "telegram:user:42", components: 5},
		"agent over Telegram": {token: testToken, admin: "telegram:user:42", agent: true, components: 18},
		"agent, no transport": {agent: true, components: 16},
	} {
		t.Run(name, func(t *testing.T) {
			b := testConfig(t)
			b.BotToken, b.BotAPIURL, b.Admin = tc.token, fake.URL(), tc.admin
			if tc.agent {
				b.Agent = AgentSettings{Models: models, Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC, Workspace: t.TempDir()}
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
	b := agentConfig(t)
	b.Agent.Models = orModels(t, model.URL(), "m1", "m2")
	return b
}

// parseModels reads section as the models: section of ganglion.yaml.
func parseModels(t *testing.T, section string) *settings.Models {
	t.Helper()
	f, err := settings.Parse([]byte("models:\n  " + strings.ReplaceAll(strings.TrimSpace(section), "\n", "\n  ") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return f.Models
}

// orModels is the models of one OpenAI-compatible provider, or, at url with
// its key in OR_KEY: ids are its models with tools, tried for chat in order.
func orModels(t *testing.T, url string, ids ...string) *settings.Models {
	t.Helper()
	var ms, chat []string
	for _, id := range ids {
		ms = append(ms, "  - {provider: or, name: "+id+", tools: true, context: 64000}")
		chat = append(chat, "{model: "+id+"}")
	}
	return parseModels(t, "providers:\n  - {name: or, type: openai, base_url: \""+url+"\", key: env:OR_KEY}\nmodels:\n"+
		strings.Join(ms, "\n")+"\npurposes: {chat: ["+strings.Join(chat, ", ")+"]}\n")
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
	want := []string{"cells.get", "cells.list", "cells.set", "host.exec", "host.stats", "message.send", "plan.show", "plan.step", "schedule.create", "schedule.delete",
		"schedule.list", "schedule.run_now", "web.fetch", "web.search", "logs.search", "journal.search", "turns.failed", "skill.read", "language.set", "secret.request",
		"destination.list", "destination.set", "destination.remove", "module.notify_to", "group.disconnect",
		"config.propose", "config.history", "config.rollback", "mcp.connect", "mcp.rule"}
	if _, err := exec.LookPath("git"); err == nil {
		// The module tools need the workspace's git too; module.import is
		// among them on every node, since the binary ships the templates.
		want = append(want, "memory.audit", "memory.forget", "memory.read", "memory.recall", "memory.write", "persona.propose",
			"module.build", "module.import", "module.install", "module.read", "module.status", "module.write")
	}
	// host.exec is registered always and on offer only while the sandbox is
	// ready (R-CMD-1, R-CMD-3).
	offered, ready := len(want), readyOf(do.MustInvoke[*sandbox.Sandbox](i))
	if !ready {
		offered--
	}
	if reg.Offered("host.exec", true) != ready {
		t.Errorf("host.exec offered %v, the sandbox ready %v", !ready, ready)
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
	if got := len(reg.Specs(true)); got != offered {
		t.Errorf("%d tools on offer to an admin, want %d", got, offered)
	}
	comps, err := do.Invoke[[]node.Component](i)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name())
	}
	for _, name := range []string{"sender", "loop", "prune", "prune-journal", "expire-pending", "scheduler", "janitor"} {
		if !slices.Contains(names, name) {
			t.Errorf("component %s is missing from %v", name, names)
		}
	}
}

// The places list has the claude work folder beside the database, with an
// age, and the janitor is the same list's.
func TestPlacesHoldTheClaudeFolderBesideTheDatabase(t *testing.T) {
	b := agentConfig(t)
	b.ClaudeDir = filepath.Join(filepath.Dir(b.DB), "claude")
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	places, err := do.Invoke[[]host.Place](i)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range places {
		if p.Name == host.PlaceClaude {
			if p.Path != b.ClaudeDir || p.MaxAge != leftoverAge {
				t.Fatalf("claude place = %+v", p)
			}
			return
		}
	}
	t.Fatalf("no claude place in %+v", places)
}

// host.stats shows the releases folder beside the database, and never ages
// it: the janitor leaves the node's releases alone.
func TestPlacesHoldTheReleasesFolderBesideTheDatabase(t *testing.T) {
	b := agentConfig(t)
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	places, err := do.Invoke[[]host.Place](i)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range places {
		if p.Name == host.PlaceReleases {
			if p.Path != filepath.Join(filepath.Dir(b.DB), "releases") || p.MaxAge != 0 {
				t.Fatalf("releases place = %+v", p)
			}
			return
		}
	}
	t.Fatalf("no releases place in %+v", places)
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
	if sys, _ := reqs[0].Messages[0]["content"].(string); sys != agent.CoreRules+"\n"+agent.SelfLine(buildinfo.Read().Short())+"\n\n"+agent.DefaultPersona {
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

// The logger and the runner hide the same secrets: one list.
func TestSecretValuesListsEverySecretTheNodeRead(t *testing.T) {
	b := Bootstrap{BotToken: "tok", BackupSecret: "bak", Agent: AgentSettings{BraveKey: "brave", Keys: map[string]string{"a": "k1", "b": "k2"}}}
	got := b.SecretValues()
	slices.Sort(got)
	if want := []string{"bak", "brave", "k1", "k2", "tok"}; !slices.Equal(got, want) {
		t.Fatalf("SecretValues = %q, want %q", got, want)
	}
}

// A skill script never gets a variable the file names for a secret, the
// bot token and the search key included, whatever it is called
// (R-RTR-5, ADR-0042). The lab sets the models without a file.
func TestScriptDenyListsEverySecretReference(t *testing.T) {
	f, err := settings.Parse([]byte("telegram: {token: env:BOT}\nagent: {search: {brave_key: env:SEARCH}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	lab := &settings.Models{Providers: []settings.Provider{{Name: "or", Key: "env:LAB"}, {Name: "local"}}}
	got := nodeSecrets(Bootstrap{Settings: f, Agent: AgentSettings{Models: lab}})
	if want := []string{"BOT", "GANGLION_MASTER_KEY", "LAB", "SEARCH"}; !slices.Equal(got, want) {
		t.Fatalf("nodeSecrets = %q, want %q", got, want)
	}
}

// readyOf reports whether s runs commands.
func readyOf(s *sandbox.Sandbox) bool { ok, _ := s.Ready(); return ok }

// hostSandboxReady reports whether a node started by this test would have
// its sandbox ready: the same checks, on this kernel.
func hostSandboxReady() bool {
	return readyOf(sandbox.New(selfImage, sandbox.Policy{}, sandbox.HostKernel(), time.Now()))
}

// A read go version must not download and run a toolchain a go.mod in the
// command's folder names: commands get GOTOOLCHAIN=local, and the whole
// environment is the fixed list.
func TestCommandEnvIsFixedAndKeepsGoLocal(t *testing.T) {
	want := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/o", "LANG=C.UTF-8", "TZ=Europe/Kyiv", "TERM=dumb", "GOTOOLCHAIN=local"}
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Skip("no time zone data")
	}
	if got := commandEnv("/home/o/", kyiv); !slices.Equal(got, want) {
		t.Fatalf("commandEnv = %q, want %q", got, want)
	}
}
