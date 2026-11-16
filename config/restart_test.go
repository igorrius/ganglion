package config

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/restart"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// A restart asked from the chat leaves a record beside the database, and
// the next start tells that conversation the node is back, naming the
// change from the chat that took effect (#238).
func TestTheNextStartSaysItIsBack(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the git binary is not installed")
	}
	b := agentConfig(t)
	b.SettingsPath = filepath.Join(t.TempDir(), settings.FileName)
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	h := do.MustInvoke[confighist.History](i)
	err := h.Record(context.Background(), confighist.SourcePropose, "more room", func() error {
		return settings.SetPath(b.SettingsPath, []settings.Change{{Path: "agent.limits.daily_turns", Value: float64(300)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := do.MustInvoke[restart.Asker](i).Ask("telegram:user:42", "telegram", "telegram:42"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(b.DB), restart.FileName)); err != nil {
		t.Fatal(err)
	}
	comps := do.MustInvoke[Restarts](i)
	if len(comps) != 1 {
		t.Fatalf("components %v", comps)
	}
	if err := comps[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	due, _, err := do.MustInvoke[*sqlqueue.Queue](i).Due(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Msg.Conv != "telegram:42" || !strings.Contains(due[0].Msg.Text, "back") || !strings.Contains(due[0].Msg.Text, "more room") {
		t.Fatalf("outbox %+v", due)
	}
}
