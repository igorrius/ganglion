package config

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/samber/do/v2"
)

// The core role starts the embedded bus with its store where the bootstrap
// says, has the bucket and the streams in place, and shows in /status.
func TestCoreRoleBuildsWithTheBus(t *testing.T) {
	b := testConfig(t)
	b.BusDir = bus.StorePath(filepath.Dir(b.DB))
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	if _, err := do.Invoke[[]node.Component](i); err != nil {
		t.Fatal(err)
	}
	nb := do.MustInvoke[*natsbus.Bus](i)
	if h := nb.Health(context.Background()); h.Mode != bus.ModeEmbedded || !h.Connected {
		t.Fatalf("health = %+v", h)
	}
	if _, err := nb.JetStream().Stream(context.Background(), bus.StreamNotify); err != nil {
		t.Fatalf("Ensure did not run: %v", err)
	}
	if _, err := os.Stat(b.BusDir); err != nil {
		t.Fatalf("the store folder: %v", err)
	}
	checks, err := nb.Status(context.Background(), status.Scope{})
	if err != nil || len(checks) != 1 || checks[0].State != status.OK {
		t.Fatalf("status = %+v, %v", checks, err)
	}
	if rep := i.Shutdown(); !rep.Succeed {
		t.Fatalf("shutdown: %v", rep.Error())
	}
}

// A bus that cannot be reached stops the start with an error that names it.
func TestUnreachableBusStopsTheStart(t *testing.T) {
	b := testConfig(t)
	b.Settings.Bus = settings.Bus{Server: string(bus.ModeExternal), URLs: []string{"nats://127.0.0.1:1"}}
	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	if _, err := do.Invoke[[]node.Component](i); err == nil {
		t.Fatal("the node started without its bus")
	}
	_ = i.Shutdown()
}

func TestBusConfigFollowsTheSettings(t *testing.T) {
	if cfg := busConfig(Bootstrap{BusDir: "/data/bus"}); cfg.Mode != bus.ModeEmbedded || cfg.StoreDir != "/data/bus" {
		t.Errorf("default = %+v", cfg)
	}
	b := Bootstrap{BusCredentials: "creds", Settings: settings.File{Bus: settings.Bus{Server: "external", URLs: []string{"tls://nats.example.com"}}}}
	if cfg := busConfig(b); cfg.Mode != bus.ModeExternal || len(cfg.URLs) != 1 || cfg.Credentials != "creds" {
		t.Errorf("external = %+v", cfg)
	}
}
