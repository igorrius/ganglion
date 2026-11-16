package config

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/modhost"
	"github.com/samber/do/v2"
)

// The module host is built at start, and with it the history of the
// node's file: a file changed on the host is committed then (R-CFG-13).
func TestStartRecordsTheNodesFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed")
	}
	b := agentConfig(t)
	b.SettingsPath = filepath.Join(t.TempDir(), "ganglion.yaml")
	const src = "log:\n  level: info\n"
	if err := os.WriteFile(b.SettingsPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	if _, err := do.Invoke[*modhost.Host](i); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(b.Agent.Workspace, filepath.FromSlash(confighist.CopyPath)))
	if err != nil || string(copied) != src {
		t.Fatalf("the copy = %q, %v", copied, err)
	}
	cs, err := do.MustInvoke[confighist.History](i).Changes(context.Background(), 10)
	if err != nil || len(cs) != 1 || cs[0].Source != confighist.SourceHost {
		t.Fatalf("Changes = %+v, %v", cs, err)
	}
}

func TestNoGitKeepsNoHistory(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	h, err := do.Invoke[confighist.History](i)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.(confighist.NoHistory); !ok {
		t.Fatalf("history without git = %T", h)
	}
	if _, err := do.Invoke[*modhost.Host](i); err != nil {
		t.Fatal(err)
	}
}
