package natsbus

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nkeys"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// hubForEdges opens a core's bus with a leaf listener on a free port.
func hubForEdges(t *testing.T, dir string) (*Bus, int) {
	t.Helper()
	cfg := DefaultConfig(dir)
	cfg.Chain = mustChain(t, newMemStore(), time.Now)
	cfg.testLeafPort = freePort(t)
	b, err := Open(context.Background(), cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b, cfg.testLeafPort
}

// edgeCreds writes the .creds file of a new edge user of hub's chain.
func edgeCreds(t *testing.T, hub *Bus, name string, perms UserPermissions) string {
	t.Helper()
	kp, _ := nkeys.CreateUser()
	pub, _ := kp.PublicKey()
	tok, err := hub.cfg.Chain.IssueUser(name, pub, perms, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := kp.Seed()
	creds := "-----BEGIN NATS USER JWT-----\n" + tok + "\n------END NATS USER JWT------\n\n" +
		"-----BEGIN USER NKEY SEED-----\n" + string(seed) + "\n------END USER NKEY SEED------\n"
	path := filepath.Join(t.TempDir(), name+".creds")
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openEdge(t *testing.T, dir, name, url, creds string) *Bus {
	t.Helper()
	cfg := DefaultConfig(dir)
	cfg.Leaf = &Leaf{Domain: name, URL: url, CredsFile: creds}
	b, err := Open(context.Background(), cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

// edgePerms are what the core lets an edge's user do for its events to be
// sourced (ADR-0114): publish the source's deliveries and replies, and take
// the requests aimed at its own JetStream domain.
func edgePerms(edge string) UserPermissions {
	return UserPermissions{
		Publish:   []string{"$JS.S.>", "$JSC.R.>", "g.tel." + edge},
		Subscribe: []string{"$JS." + edge + ".API.>"},
	}
}

func eventsHeld(t *testing.T, b *Bus) uint64 {
	t.Helper()
	s, err := b.JetStream().Stream(context.Background(), bus.StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return i.State.Msgs
}

func publishEdgeEvent(t *testing.T, b *Bus, edge string, n int) {
	t.Helper()
	subj, _ := bus.Event(edge, "m", "p")
	// No Nats-Msg-Id: the duplicate window must not hide a second copy.
	if _, err := b.JetStream().Publish(context.Background(), subj, []byte(strconv.Itoa(n))); err != nil {
		t.Fatal(err)
	}
}

// What an edge keeps arrives in the core's stream once: no copy comes by
// the plain subject as well, and the edge's own stream is its own.
func TestEdgeEventsReachTheCoreOnce(t *testing.T) {
	ctx := context.Background()
	hub, port := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	creds := edgeCreds(t, hub, "e1", edgePerms("e1"))
	edge := openEdge(t, filepath.Join(t.TempDir(), "edge"), "e1", "nats-leaf://127.0.0.1:"+itoa(port), creds)
	if err := hub.SourceEdge(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	const n = 300
	for i := range n {
		publishEdgeEvent(t, edge, "e1", i)
	}
	waitFor(t, func() bool { return eventsHeld(t, hub) >= n })
	time.Sleep(500 * time.Millisecond) // a late duplicate would show now
	if got := eventsHeld(t, hub); got != n {
		t.Fatalf("the core holds %d events, want %d", got, n)
	}
	if got := eventsHeld(t, edge); got != n {
		t.Fatalf("the edge holds %d events, want %d", got, n)
	}
}

// The R-EDGE-7 gate: while the link is down the edge keeps its events; when
// it returns the core has them all, in order, none twice.
func TestEdgeKeepsEventsWhileTheLinkIsDown(t *testing.T) {
	ctx := context.Background()
	seeds := newMemStore()
	hubDir, port := filepath.Join(t.TempDir(), "hub"), freePort(t)
	openHub := func() *Bus {
		cfg := DefaultConfig(hubDir)
		cfg.Chain = mustChain(t, seeds, time.Now)
		cfg.testLeafPort = port
		b, err := Open(ctx, cfg, quiet())
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		return b
	}
	hub := openHub()
	creds := edgeCreds(t, hub, "e1", edgePerms("e1"))
	edge := openEdge(t, filepath.Join(t.TempDir(), "edge"), "e1", "nats-leaf://127.0.0.1:"+itoa(port), creds)
	if err := hub.SourceEdge(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return edge.EdgeHealth(ctx).Linked })
	for i := range 3 {
		publishEdgeEvent(t, edge, "e1", i)
	}
	waitFor(t, func() bool { return eventsHeld(t, hub) == 3 })

	if err := hub.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !edge.EdgeHealth(ctx).Linked })
	for i := 3; i < 8; i++ {
		publishEdgeEvent(t, edge, "e1", i)
	}
	if h := edge.EdgeHealth(ctx); h.Linked || h.Events != 8 {
		t.Fatalf("edge health = %+v, want unlinked and 8 events held", h)
	}

	hub = openHub()
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })
	waitFor(t, func() bool { return edge.EdgeHealth(ctx).Linked })
	if err := hub.ResyncEdge(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return eventsHeld(t, hub) >= 8 })
	time.Sleep(500 * time.Millisecond)
	s, err := hub.JetStream().Stream(ctx, bus.StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 8 {
		t.Fatalf("the core holds %d events, want 8", info.State.Msgs)
	}
	for seq := uint64(1); seq <= 8; seq++ {
		m, err := s.GetMsg(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		if string(m.Data) != strconv.Itoa(int(seq-1)) {
			t.Fatalf("event %d is %q: out of order", seq, m.Data)
		}
	}
}

// Ensure over a core that sources an edge keeps the source; sourcing twice
// does nothing; unsourcing removes it.
func TestEdgeSourcesAreKeptByEnsure(t *testing.T) {
	ctx := context.Background()
	hub, _ := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	for range 2 {
		if err := hub.SourceEdge(ctx, "e1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.SourceEdge(ctx, "e2"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		src, err := hub.EdgeSources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range src {
			out = append(out, s.Edge)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"e1", "e2"}) {
		t.Fatalf("sources = %v, want e1 e2", got)
	}
	if err := hub.UnsourceEdge(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"e2"}) {
		t.Fatalf("sources = %v, want e2", got)
	}
	if err := hub.SourceEdge(ctx, "a.b"); !errors.Is(err, bus.ErrBadToken) {
		t.Fatalf("a bad name: %v", err)
	}
}

func TestLeafConfigRefusals(t *testing.T) {
	chain := mustChain(t, newMemStore(), time.Now)
	cases := map[string]Config{
		"no domain":      {Mode: bus.ModeEmbedded, Leaf: &Leaf{}},
		"url, no creds":  {Mode: bus.ModeEmbedded, Leaf: &Leaf{Domain: "e1", URL: "nats-leaf://127.0.0.1:1"}},
		"with the chain": {Mode: bus.ModeEmbedded, Chain: chain, Leaf: &Leaf{Domain: "e1"}},
		"external":       {Mode: bus.ModeExternal, URLs: []string{"nats://127.0.0.1:1"}, Leaf: &Leaf{Domain: "e1"}},
	}
	for name, cfg := range cases {
		if err := cfg.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&Bus{cfg: Config{Leaf: &Leaf{Domain: "e1"}}}).SourceEdge(context.Background(), "e2"); err == nil {
		t.Error("an edge sourced another edge")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
