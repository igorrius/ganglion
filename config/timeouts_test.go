package config

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/samber/do/v2"
)

// Ruling 17 of M4c: the file's timeouts reach the agent's calls and turns
// and the loop's handler.
func TestTheTimeoutsReachTheAgentAndTheLoop(t *testing.T) {
	b := agentConfig(t)
	b.Timeouts = Timeouts{Call: 70 * time.Second, Turn: 4 * time.Minute, Handler: 5 * time.Minute}
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	cfg, _ := agentParts(i)
	if cfg.CallTimeout != 70*time.Second || cfg.TurnTimeout != 4*time.Minute {
		t.Fatalf("agent config %+v", cfg)
	}
	if got := do.MustInvoke[*core.Loop](i).TurnTimeout(); got != 5*time.Minute {
		t.Fatalf("the loop's turn timeout %v", got)
	}
}

// The build queue is a component of an agent node and the agent reads it.
func TestTheBuildQueueRunsAndTheAgentReadsIt(t *testing.T) {
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	q := do.MustInvoke[*builtin.BuildQueue](i)
	found := false
	for _, c := range do.MustInvoke[Housekeeping](i) {
		found = found || c == any(q)
	}
	if !found {
		t.Fatal("the build queue is not a running component")
	}
	if _, deps := agentParts(i); deps.Builds != q {
		t.Fatal("the agent does not read the build queue")
	}
}

// testTimeouts are the settings' default timeouts, as cmd maps them.
func testTimeouts() Timeouts {
	a := settings.Default().Agent
	return Timeouts{Call: time.Duration(a.CallTimeout), Turn: time.Duration(a.TurnTimeout), Handler: a.HandlerTimeout()}
}
