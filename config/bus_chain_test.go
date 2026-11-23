package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/sqlitedb"
)

// lockedVault is a store opened on a key that does not fit its rows.
func lockedVault(t *testing.T) *vault.Vault {
	t.Helper()
	ctx := context.Background()
	conn, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := vault.Open(ctx, conn, vault.Key{1}, time.Now); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(ctx, conn, vault.Key{2}, time.Now)
	if err != nil || !v.Locked() {
		t.Fatalf("locked = %v, %v", v != nil && v.Locked(), err)
	}
	return v
}

// A locked store leaves the embedded bus without its chain and says so;
// an external server on the chain cannot do without it; an external server
// without bus.trust never reads the store.
func TestBusChainWithALockedStore(t *testing.T) {
	ctx := context.Background()
	v := lockedVault(t)
	cfg := natsbus.DefaultConfig("")
	if err := withChain(ctx, &cfg, Bootstrap{}, v); err != nil || cfg.Chain != nil || !cfg.ChainLocked {
		t.Fatalf("embedded: chain %v, locked %v, %v", cfg.Chain != nil, cfg.ChainLocked, err)
	}
	b := Bootstrap{Settings: settings.File{Bus: settings.Bus{Server: string(bus.ModeExternal), URLs: []string{"nats://x.example.com:4222"}, Trust: settings.TrustChain}}}
	cfg = busConfig(b)
	if err := withChain(ctx, &cfg, b, v); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("external on the chain: %v", err)
	}
	b.Settings.Bus.Trust = ""
	cfg = busConfig(b)
	if err := withChain(ctx, &cfg, b, v); err != nil || cfg.Chain != nil || cfg.ChainLocked {
		t.Fatalf("external without trust: %v", err)
	}
}
