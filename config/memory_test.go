package config

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/samber/do/v2"
)

func agentConfig(t *testing.T) Bootstrap {
	t.Helper()
	catalog, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: or, type: openai, base_url: http://127.0.0.1:1, key_env: OR_KEY}\n" +
		"models:\n  - {provider: or, name: m1, tier: cheap, context: 64000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := testConfig(t)
	b.Admin = "telegram:user:42"
	b.Agent = AgentSettings{Catalog: &catalog, Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC, Workspace: t.TempDir()}
	return b
}

func TestMemoryOnRegistersToolsAndTheAudit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed; this test needs memory on (ADR-0033)")
	}
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	reg, err := do.Invoke[*tool.Registry](i)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Spec("memory.write"); !ok {
		t.Fatal("memory.write is not registered")
	}
	j, err := do.MustInvoke[*schedule.Store](i).Get(context.Background(), "system:memory-audit")
	if err != nil {
		t.Fatal(err)
	}
	if !j.System || j.Owner != "telegram:user:42" || j.Conv != "telegram:42" || j.Transport != "telegram" {
		t.Fatalf("audit job %+v", j)
	}
}

func TestMemoryOffWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	logs := &bytes.Buffer{}
	i := New(context.Background(), agentConfig(t), slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { _ = i.Shutdown() })
	reg, err := do.Invoke[*tool.Registry](i)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Spec("memory.write"); ok {
		t.Fatal("memory tools without git")
	}
	if _, err := do.Invoke[*agent.Agent](i); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "memory is off") || !strings.Contains(logs.String(), "git binary is not installed") {
		t.Fatalf("no warning: %s", logs)
	}
	if _, err := do.MustInvoke[*schedule.Store](i).Get(context.Background(), "system:memory-audit"); err == nil {
		t.Fatal("an audit without memory")
	}
}
