package natsbus

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// The edges' entry fails closed: it exists only for an embedded server
// that has a chain and was asked for it (ADR-0114).
func TestWebSocketEntryOnlyWithAChainAndTheFlag(t *testing.T) {
	open := func(t *testing.T, mod func(*Config)) *Bus {
		t.Helper()
		cfg := DefaultConfig(t.TempDir())
		mod(&cfg)
		b, err := Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
		return b
	}
	chain := mustChain(t, newMemStore(), time.Now)
	if a := open(t, func(c *Config) { c.Chain, c.WebSocket = chain, true }).WebSocketAddr(); a == "" {
		t.Error("a chain and the flag gave no entry")
	}
	if a := open(t, func(c *Config) { c.Chain = chain }).WebSocketAddr(); a != "" {
		t.Errorf("an entry without the flag: %s", a)
	}
	if a := open(t, func(c *Config) { c.WebSocket = true }).WebSocketAddr(); a != "" {
		t.Errorf("an entry without a chain: %s", a)
	}
	if a := open(t, func(c *Config) { c.WebSocket, c.ChainLocked = true, true }).WebSocketAddr(); a != "" {
		t.Errorf("an entry with the store locked: %s", a)
	}
}
