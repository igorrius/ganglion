package config

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// --roles edge runs the module host and the telemetry on a leaf of its
// own, and no Telegram even with a token set (R-EDGE-7).
func TestEdgeRunsWithoutTelegram(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.Settings.Edge.Name = "garage"
	var logs bytes.Buffer
	r := runNode(t, []node.Role{node.Edge}, cfg, slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil)))
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	eventually(t, "the edge's start", func() bool { return strings.Contains(logs.String(), "node: edge on") })
	time.Sleep(300 * time.Millisecond)
	r.cancel()
	if err := r.result(t); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if len(fake.Sent()) != 0 {
		t.Fatalf("an edge answered in Telegram: %+v", fake.Sent())
	}
}

// An edge needs a name, and a link needs the edge's credentials.
func TestEdgeStartRefusals(t *testing.T) {
	cfg := testConfig(t)
	err := Run(context.Background(), []node.Role{node.Edge}, cfg, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "edge.name") {
		t.Fatalf("no name: %v", err)
	}
	cfg.Settings.Edge.Name, cfg.Settings.Edge.URL = "garage", "wss://example.com/bus"
	err = Run(context.Background(), []node.Role{node.Edge}, cfg, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "credentials file") {
		t.Fatalf("no credentials: %v", err)
	}
}

func TestEdgeOnly(t *testing.T) {
	for _, c := range []struct {
		roles []node.Role
		want  bool
	}{{[]node.Role{node.Edge}, true}, {[]node.Role{node.Edge, node.Worker}, true}, {[]node.Role{node.Core, node.Edge}, false}, {[]node.Role{node.Core}, false}} {
		if got := EdgeOnly(c.roles); got != c.want {
			t.Errorf("EdgeOnly(%v) = %v", c.roles, got)
		}
	}
}
