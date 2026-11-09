package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/internal/testinputs"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/runner/runnertest"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

const wiredManifest = `name: probe
version: 1
triggers:
  - cron: "*/30 * * * *"
  - tool: status
    about: The probe's clock.
    output: {unix: integer}
cells:
  report:
    soc: {kind: sensor, type: integer, about: "Charge, %"}
config:
  op_cron: {type: string, default: '{"op":"cell_report","name":"soc","json":87}'}
  op_tool_status: {type: string, default: '{"op":"now"}'}
`

// An installed local module runs in a built node: its tool is in the
// registry, its cron job is in the schedule, ModuleCron reports its cell,
// /status counts it, and turning it off in the settings file stops it.
func TestTheNodeHostsALocalModule(t *testing.T) {
	testinputs.Build(t, "../runner/runnertest/probe/wasm", "GOOS=wasip1", "GOARCH=wasm")
	wasm, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	b := testConfig(t)
	b.BotToken, b.BotAPIURL, b.Admin = testToken, fake.URL(), "telegram:user:42"
	b.ModulesDir = t.TempDir()
	b.Agent = AgentSettings{Models: parseModels(t, "providers:\n  - {name: or, type: openai, base_url: \""+model.URL()+
		"\", key: env:OR_KEY}\nmodels:\n  - {provider: or, name: m1, context: 64000}\npurposes: {chat: [{model: m1}]}\n"),
		Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC, Workspace: t.TempDir()}
	ctx := context.Background()
	i := New(ctx, b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })

	st := do.MustInvoke[*modstore.Store](i)
	build := modstore.Build{Module: "local/probe", Wasm: wasm, Manifest: []byte(wiredManifest), Language: "go", By: "telegram:user:42"}
	if o, err := st.InstallLocal(ctx, build, modstore.Digest(build.Wasm, build.Manifest)); err != nil || o.Result != modstore.ResultInstalled {
		t.Fatalf("install %+v, %v", o, err)
	}

	reg := do.MustInvoke[*tool.Registry](i)
	h, ok := reg.Lookup("probe.status")
	if !ok {
		t.Fatal("the module's tool is not in the registry")
	}
	res, err := h.Run(ctx, tool.Call{Name: "probe.status", Args: json.RawMessage(`{}`), Admin: true})
	if err != nil || !strings.HasPrefix(res.Text, `{"unix":`) {
		t.Fatalf("tool %+v, %v", res, err)
	}
	j, err := do.MustInvoke[*schedule.Store](i).Get(ctx, "module:local/probe:0")
	if err != nil || j.Kind != schedule.KindModule || j.Owner != "telegram:user:42" || j.Deleted {
		t.Fatalf("job %+v, %v", j, err)
	}
	if err := agentDeps(i).ModuleCron(ctx, "local/probe"); err != nil {
		t.Fatal(err)
	}
	if c, err := do.MustInvoke[*cells.Service](i).Get(ctx, "probe.soc"); err != nil || string(c.Reported) != "87" || c.Owner != "local/probe" {
		t.Fatalf("cell %+v, %v", c, err)
	}
	var line bool
	for _, s := range do.MustInvoke[[]status.Source](i) {
		if s.Name() == "modules-local" {
			cs, _ := s.Status(ctx, status.Scope{})
			line = len(cs) == 1 && cs[0].Vars["running"] == "1"
		}
	}
	if !line {
		t.Fatal("/status does not count the module")
	}

	if err := os.WriteFile(settingsFile(b), []byte("modules:\n  - name: local/probe\n    off: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := do.MustInvoke[*modhost.Host](i).Reload(ctx, "local/probe"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("probe.status"); ok {
		t.Fatal("an off module's tool is still offered")
	}
}

// agentDeps are the agent's collaborators as the container builds them.
func agentDeps(i do.Injector) agent.Deps {
	_, d := agentParts(i)
	return d
}

func TestExamplesDirIsTheNamedFolderWhenItExists(t *testing.T) {
	dir := t.TempDir()
	if got := examplesDir(Bootstrap{Examples: dir}); got != dir {
		t.Fatalf("examples %q, want %q", got, dir)
	}
	if got := examplesDir(Bootstrap{Examples: filepath.Join(dir, "missing")}); got != "" {
		t.Fatalf("a missing folder is %q", got)
	}
}
