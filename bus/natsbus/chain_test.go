package natsbus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/secrets"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// memStore is a SeedStore in memory that counts its writes.
type memStore struct {
	mu   sync.Mutex
	m    map[string]string
	sets int
}

func newMemStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Get(_ context.Context, scope secrets.Scope, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[string(scope)+"/"+name], nil
}

func (s *memStore) Set(_ context.Context, scope secrets.Scope, name, value, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(scope)+"/"+name] = value
	s.sets++
	return nil
}

func unlocked() bool { return false }

func loadChain(t *testing.T, s SeedStore, now func() time.Time) *Chain {
	t.Helper()
	c, err := LoadChain(context.Background(), s, unlocked, now)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The chain is made once and read back the same: a restore of the store
// brings the same operator and account back.
func TestChainIsMadeOnceAndReadBack(t *testing.T) {
	s := newMemStore()
	a := loadChain(t, s, time.Now)
	if s.sets != 5 {
		t.Fatalf("sets = %d, want 5 seeds", s.sets)
	}
	for _, name := range []string{SeedOperator, SeedAccount, SeedSigning, SeedSystem, SeedCore} {
		if !strings.HasPrefix(s.m["node/"+name], "S") {
			t.Errorf("%s is not a seed", name)
		}
	}
	b := loadChain(t, s, time.Now)
	if s.sets != 5 {
		t.Fatalf("a second load wrote %d more", s.sets-5)
	}
	if a.OperatorPublicKey() != b.OperatorPublicKey() || a.AccountPublicKey() != b.AccountPublicKey() ||
		a.CorePublicKey() != b.CorePublicKey() || a.SystemAccountPublicKey() != b.SystemAccountPublicKey() {
		t.Fatal("the chain changed between loads")
	}
	if !nkeys.IsValidPublicOperatorKey(a.OperatorPublicKey()) || !nkeys.IsValidPublicAccountKey(a.AccountPublicKey()) {
		t.Fatal("wrong key kinds")
	}
	oc, err := jwt.DecodeOperatorClaims(must(a.OperatorJWT()))
	if err != nil || oc.SystemAccount != a.SystemAccountPublicKey() || oc.Subject != a.OperatorPublicKey() {
		t.Fatalf("operator claims = %+v, %v", oc, err)
	}
	ac, err := jwt.DecodeAccountClaims(must(a.AccountJWT()))
	if err != nil || ac.Issuer != a.OperatorPublicKey() || ac.Name != AccountName || len(ac.SigningKeys) != 1 {
		t.Fatalf("account claims = %+v, %v", ac, err)
	}
}

// A locked store is neither read nor written; a seed of the wrong kind or
// a broken entry stops the load and names the entry, not the value.
func TestChainRefusals(t *testing.T) {
	ctx := context.Background()
	s := newMemStore()
	if _, err := LoadChain(ctx, s, func() bool { return true }, time.Now); !errors.Is(err, ErrStoreLocked) || s.sets != 0 {
		t.Fatalf("locked: %v, sets %d", err, s.sets)
	}
	user := must(nkeys.CreateUser())
	seed := string(must(user.Seed()))
	s.m["node/"+SeedOperator] = seed
	if _, err := LoadChain(ctx, s, unlocked, time.Now); err == nil || !strings.Contains(err.Error(), "wrong kind") || strings.Contains(err.Error(), seed) {
		t.Fatalf("wrong kind: %v", err)
	}
	s.m["node/"+SeedOperator] = "not a seed"
	if _, err := LoadChain(ctx, s, unlocked, time.Now); err == nil || !strings.Contains(err.Error(), SeedOperator) {
		t.Fatalf("garbage: %v", err)
	}
	s = newMemStore()
	s.m["node/"+RevokedName] = `{"not-a-key":1}`
	if _, err := LoadChain(ctx, s, unlocked, time.Now); err == nil || !strings.Contains(err.Error(), RevokedName) {
		t.Fatalf("revocation list: %v", err)
	}
}

func TestIssueUserRefusals(t *testing.T) {
	c := loadChain(t, newMemStore(), time.Now)
	edge := must(must(nkeys.CreateUser()).PublicKey())
	for name, tc := range map[string]struct {
		pub  string
		ttl  time.Duration
		want error
	}{
		"account key": {c.AccountPublicKey(), time.Hour, ErrBadUserKey},
		"garbage":     {"UABC", time.Hour, ErrBadUserKey},
		"core key":    {c.CorePublicKey(), time.Hour, ErrCoreKey},
		"no ttl":      {edge, 0, ErrBadTTL},
		"long ttl":    {edge, MaxUserTTL + time.Second, ErrBadTTL},
	} {
		if _, err := c.IssueUser("e", tc.pub, UserPermissions{}, tc.ttl); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if _, err := c.revoke(context.Background(), c.CorePublicKey()); !errors.Is(err, ErrCoreKey) {
		t.Errorf("revoke core: %v", err)
	}
}

// openChained opens an embedded bus on the chain in s, storing under dir.
func openChained(t *testing.T, dir string, s SeedStore) *Bus {
	t.Helper()
	cfg := DefaultConfig(dir)
	cfg.Chain = loadChain(t, s, time.Now)
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

// connectAs connects to the embedded server in-process with the given
// user JWT and seed.
func connectAs(b *Bus, tok string, kp nkeys.KeyPair) (*nats.Conn, error) {
	return nats.Connect("", nats.InProcessServer(b.srv),
		nats.UserJWT(func() (string, error) { return tok, nil }, kp.Sign),
		nats.Timeout(2*time.Second), nats.NoReconnect())
}

// The embedded server runs in operator mode on the chain: the core works
// in NET, and a connection without credentials is refused.
func TestEmbeddedOnTheChain(t *testing.T) {
	t.Parallel()
	b := openChained(t, filepath.Join(t.TempDir(), "bus"), newMemStore())
	publishOne(t, b)
	if h := b.Health(context.Background()); !h.Connected || h.StoreBytes == 0 {
		t.Fatalf("health = %+v", h)
	}
	if _, err := nats.Connect("", nats.InProcessServer(b.srv), nats.NoReconnect()); err == nil {
		t.Fatal("an anonymous connection was let in")
	}
}

// The streams and the cells bucket a node kept before the chain move into
// NET at the first start on it, and two stores at once stop the start.
func TestStoreMovesIntoTheNetworkAccount(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "bus")
	old := openEmbedded(t, dir)
	if err := old.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	kv := must(old.JetStream().KeyValue(ctx, bus.BucketCells))
	if _, err := kv.Put(ctx, "a.b", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	if err := old.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	s := newMemStore()
	b := openChained(t, dir, s)
	e, err := must(b.JetStream().KeyValue(ctx, bus.BucketCells)).Get(ctx, "a.b")
	if err != nil || string(e.Value()) != "kept" {
		t.Fatalf("after the move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "jetstream", globalAccount)); !os.IsNotExist(err) {
		t.Fatalf("$G is still there: %v", err)
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "jetstream", globalAccount), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig(dir)
	cfg.Chain = loadChain(t, s, time.Now)
	if _, err := Open(ctx, cfg, quiet()); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("two stores: %v", err)
	}
}

// An edge's JWT allows exactly what it lists; forged, mismatched, expired
// and revoked credentials are refused by the server.
func TestEdgeUserAbuse(t *testing.T) {
	ctx := context.Background()
	s := newMemStore()
	b := openChained(t, filepath.Join(t.TempDir(), "bus"), s)
	c := b.Chain()
	edge := must(nkeys.CreateUser())
	edgePub := must(edge.PublicKey())
	perms := UserPermissions{Publish: []string{"g.ev.home.>"}, Subscribe: []string{"desired.home.>"}}
	tok := must(c.IssueUser("home", edgePub, perms, time.Hour))

	t.Run("allowed and denied subjects", func(t *testing.T) {
		nc, err := connectAs(b, tok, edge)
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		errs := make(chan error, 4)
		nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err })
		if err := nc.Publish("g.ev.home.x", nil); err != nil {
			t.Fatal(err)
		}
		_ = nc.Publish("g.ev.other.x", nil)
		if err := nc.Flush(); err != nil {
			t.Fatal(err)
		}
		expectPermissionError(t, errs, "g.ev.other.x")
		if _, err := nc.SubscribeSync("cells.>"); err != nil {
			t.Fatal(err)
		}
		_ = nc.Flush()
		expectPermissionError(t, errs, "cells.>")
	})

	t.Run("no permissions allow nothing", func(t *testing.T) {
		k := must(nkeys.CreateUser())
		nc, err := connectAs(b, must(c.IssueUser("bare", must(k.PublicKey()), UserPermissions{}, time.Hour)), k)
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		errs := make(chan error, 2)
		nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err })
		_ = nc.Publish("g.ev.home.x", nil)
		_ = nc.Flush()
		expectPermissionError(t, errs, "g.ev.home.x")
	})

	t.Run("forged by another account", func(t *testing.T) {
		t.Parallel()
		rogue := must(nkeys.CreateAccount())
		uc := jwt.NewUserClaims(edgePub)
		uc.IssuerAccount = c.AccountPublicKey()
		if _, err := connectAs(b, must(uc.Encode(rogue)), edge); err == nil {
			t.Fatal("a JWT signed by a foreign key was let in")
		}
	})

	t.Run("issued by another chain", func(t *testing.T) {
		t.Parallel()
		other := loadChain(t, newMemStore(), time.Now)
		k := must(nkeys.CreateUser())
		if _, err := connectAs(b, must(other.IssueUser("x", must(k.PublicKey()), perms, time.Hour)), k); err == nil {
			t.Fatal("a JWT from another chain was let in")
		}
	})

	t.Run("someone else's JWT", func(t *testing.T) {
		t.Parallel()
		thief := must(nkeys.CreateUser())
		if _, err := connectAs(b, tok, thief); err == nil {
			t.Fatal("a JWT was used without its seed")
		}
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		past := loadChain(t, s, func() time.Time { return time.Now().Add(-2 * time.Hour) })
		k := must(nkeys.CreateUser())
		if _, err := connectAs(b, must(past.IssueUser("old", must(k.PublicKey()), perms, time.Hour)), k); err == nil {
			t.Fatal("an expired JWT was let in")
		}
	})

	t.Run("revoked", func(t *testing.T) {
		t.Parallel()
		edge := must(nkeys.CreateUser())
		edgePub := must(edge.PublicKey())
		tok := must(c.IssueUser("gone", edgePub, perms, time.Hour))
		nc, err := connectAs(b, tok, edge)
		if err != nil {
			t.Fatal(err)
		}
		closed := make(chan struct{})
		nc.SetClosedHandler(func(*nats.Conn) { close(closed) })
		if err := b.Revoke(ctx, edgePub); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the revoked user's connection stayed open")
		}
		if _, err := connectAs(b, tok, edge); err == nil {
			t.Fatal("a revoked user was let in again")
		}
		if _, err := c.IssueUser("home", edgePub, perms, time.Hour); !errors.Is(err, ErrRevoked) {
			t.Fatalf("issue to a revoked key: %v", err)
		}
		if err := b.Revoke(ctx, edgePub); err != nil {
			t.Fatalf("a second revoke: %v", err)
		}
		// The revocation is stored: a restart on the same store keeps it.
		again := loadChain(t, s, time.Now)
		if !again.Revoked(edgePub) {
			t.Fatal("the revocation was not stored")
		}
		ac := must(jwt.DecodeAccountClaims(must(again.AccountJWT())))
		if !ac.IsClaimRevoked(must(jwt.DecodeUserClaims(tok))) {
			t.Fatal("the account claims do not carry the revocation")
		}
	})
}

func expectPermissionError(t *testing.T, errs <-chan error, subject string) {
	t.Helper()
	select {
	case err := <-errs:
		if !strings.Contains(strings.ToLower(err.Error()), "permissions violation") || !strings.Contains(err.Error(), subject) {
			t.Fatalf("error = %v, want a violation for %s", err, subject)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no permissions violation for %s", subject)
	}
}

// An external server that trusts the node's operator and runs a full
// resolver gets the account claims pushed, and the node connects as the
// core's user; a revocation is pushed too.
func TestExternalServerOnTheChain(t *testing.T) {
	ctx := context.Background()
	s := newMemStore()
	c := loadChain(t, s, time.Now)
	oc := must(jwt.DecodeOperatorClaims(must(c.OperatorJWT())))
	res, err := server.NewDirAccResolver(t.TempDir(), 0, time.Minute, server.NoDelete)
	if err != nil {
		t.Fatal(err)
	}
	// The server's owner preloads the system account, as with nsc.
	if err := res.Store(c.SystemAccountPublicKey(), must(c.SystemAccountJWT())); err != nil {
		t.Fatal(err)
	}
	url, srv := startExternalServer(t, &server.Options{
		TrustedOperators: []*jwt.OperatorClaims{oc}, AccountResolver: res, SystemAccount: c.SystemAccountPublicKey(),
	})
	cfg := DefaultConfig("")
	cfg.Mode, cfg.URLs, cfg.StartTimeout, cfg.Chain = bus.ModeExternal, []string{url}, 3*time.Second, c
	b, err := Open(ctx, cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(ctx) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publishOne(t, b)

	edge := must(nkeys.CreateUser())
	edgePub := must(edge.PublicKey())
	tok := must(c.IssueUser("home", edgePub, UserPermissions{Publish: []string{"g.ev.home.>"}}, time.Hour))
	connect := func() (*nats.Conn, error) {
		return nats.Connect(url, nats.UserCredentialBytes(must(jwt.FormatUserConfig(tok, must(edge.Seed())))),
			nats.Timeout(2*time.Second), nats.NoReconnect())
	}
	nc, err := connect()
	if err != nil {
		t.Fatalf("the edge could not connect: %v", err)
	}
	nc.Close()
	if err := b.Revoke(ctx, edgePub); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		nc, err := connect()
		if err != nil {
			break
		}
		nc.Close()
		if time.Now().After(deadline) {
			t.Fatal("the external server still lets the revoked user in")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = srv
}

// An external server that does not trust the node's operator is an error
// that says what is wrong; credentials and the chain together are refused.
func TestExternalChainRefusals(t *testing.T) {
	ctx := context.Background()
	c := loadChain(t, newMemStore(), time.Now)
	url := startExternal(t, &server.Options{})
	cfg := DefaultConfig("")
	cfg.Mode, cfg.URLs, cfg.StartTimeout, cfg.Chain = bus.ModeExternal, []string{url}, time.Second, c
	if _, err := Open(ctx, cfg, quiet()); err == nil || !strings.Contains(err.Error(), "push account claims") {
		t.Fatalf("no operator: %v", err)
	}
	cfg.Credentials = "x"
	if _, err := Open(ctx, cfg, quiet()); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both: %v", err)
	}
}
