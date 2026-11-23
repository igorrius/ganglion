package natsbus_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/bus/wsproxy"
	"github.com/igorrius/ganglion/secrets"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

type store struct {
	mu sync.Mutex
	m  map[string]string
}

func newStore() *store { return &store{m: map[string]string{}} }

func (s *store) Get(_ context.Context, sc secrets.Scope, n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[string(sc)+"/"+n], nil
}

func (s *store) Set(_ context.Context, sc secrets.Scope, n, v, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(sc)+"/"+n] = v
	return nil
}

func chainOn(t *testing.T, s *store, now func() time.Time) *natsbus.Chain {
	t.Helper()
	c, err := natsbus.LoadChain(context.Background(), s, func() bool { return false }, now)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rig is a real embedded bus in operator mode behind the proxy, served as
// the public listener would.
type rig struct {
	bus   *natsbus.Bus
	chain *natsbus.Chain
	store *store
	url   string // ws://host:port/bus
	host  string
}

func newRig(t *testing.T, tunnels int, mod func(*natsbus.Config)) *rig {
	t.Helper()
	st := newStore()
	ch := chainOn(t, st, time.Now)
	cfg := natsbus.DefaultConfig(t.TempDir())
	cfg.Chain, cfg.WebSocket, cfg.AuthTimeout = ch, true, 400*time.Millisecond
	if mod != nil {
		mod(&cfg)
	}
	b, err := natsbus.Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	wc := wsproxy.DefaultConfig()
	wc.MaxTunnels = tunnels
	h, err := wsproxy.New(b.WebSocketAddr(), wc, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	host := strings.TrimPrefix(ts.URL, "http://")
	return &rig{bus: b, chain: ch, store: st, url: "ws://" + host + wsproxy.Path, host: host}
}

// edge makes a user key and a JWT for it from chain c.
func edge(t *testing.T, c *natsbus.Chain) (nkeys.KeyPair, string) {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := kp.PublicKey()
	tok, err := c.IssueUser("edge", pub, natsbus.UserPermissions{Publish: []string{"g.ev.edge.>"}, Subscribe: []string{"desired.edge.>"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return kp, tok
}

func dial(url string, kp nkeys.KeyPair, tok string, extra ...nats.Option) (*nats.Conn, error) {
	opts := []nats.Option{nats.Timeout(3 * time.Second), nats.MaxReconnects(0), nats.NoReconnect()}
	if kp != nil {
		opts = append(opts, nats.UserJWT(func() (string, error) { return tok, nil }, kp.Sign))
	}
	return nats.Connect(url, append(opts, extra...)...)
}

// A valid JWT passes the proxy and the user does what its JWT allows.
func TestValidJWTConnectsAndIsHeldToItsPermissions(t *testing.T) {
	r := newRig(t, 8, nil)
	kp, tok := edge(t, r.chain)
	errs := make(chan error, 4)
	nc, err := dial(r.url, kp, tok, nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) { errs <- e }))
	if err != nil {
		t.Fatalf("a valid edge was refused: %v", err)
	}
	defer nc.Close()
	if err := nc.Publish("g.ev.edge.x", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("g.ev.other.x", []byte("no")); err != nil {
		t.Fatal(err) // refused by the server, asynchronously
	}
	_ = nc.Flush()
	select {
	case e := <-errs:
		if !strings.Contains(strings.ToLower(e.Error()), "permissions violation") {
			t.Fatalf("unexpected error %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a subject outside the JWT's permissions was not refused")
	}
}

func TestNoJWTIsRefused(t *testing.T) {
	r := newRig(t, 8, nil)
	nc, err := dial(r.url, nil, "")
	if err == nil {
		nc.Close()
		t.Fatal("a connection with no JWT was accepted")
	}
	if !errors.Is(err, nats.ErrAuthorization) && !strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("refused for another reason than authorization: %v", err)
	}
}

func TestExpiredJWTIsRefused(t *testing.T) {
	r := newRig(t, 8, nil)
	old := chainOn(t, r.store, func() time.Time { return time.Now().Add(-3 * time.Hour) }) // the same keys, a clock in the past
	kp, tok := edge(t, old)                                                                // valid for an hour from three hours ago
	nc, err := dial(r.url, kp, tok)
	if err == nil {
		nc.Close()
		t.Fatal("an expired JWT was accepted")
	}
}

func TestForgedJWTIsRefused(t *testing.T) {
	r := newRig(t, 8, nil)
	other := chainOn(t, newStore(), time.Now) // another network's chain
	kp, tok := edge(t, other)
	if nc, err := dial(r.url, kp, tok); err == nil {
		nc.Close()
		t.Fatal("a JWT from another operator's chain was accepted")
	}
	// A JWT signed by a key of its own that claims our account.
	acc := r.chain.AccountPublicKey()
	rogue, _ := nkeys.CreateAccount()
	user, _ := nkeys.CreateUser()
	upub, _ := user.PublicKey()
	uc := jwt.NewUserClaims(upub)
	uc.IssuerAccount = acc
	forged, err := uc.Encode(rogue)
	if err != nil {
		t.Fatal(err)
	}
	if nc, err := dial(r.url, user, forged); err == nil {
		nc.Close()
		t.Fatal("a JWT signed by a key that is not the signing key was accepted")
	}
}

// A copied JWT without its seed cannot sign the server's nonce.
func TestJWTWithAnotherKeysSignatureIsRefused(t *testing.T) {
	r := newRig(t, 8, nil)
	_, tok := edge(t, r.chain)
	thief, _ := nkeys.CreateUser()
	if nc, err := dial(r.url, thief, tok); err == nil {
		nc.Close()
		t.Fatal("a copied JWT with a wrong nonce signature was accepted")
	}
}

func TestRevokedUserIsDroppedAndRefused(t *testing.T) {
	r := newRig(t, 8, nil)
	kp, tok := edge(t, r.chain)
	closed := make(chan struct{})
	nc, err := dial(r.url, kp, tok, nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	pub, _ := kp.PublicKey()
	if err := r.bus.Revoke(context.Background(), pub); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a revoked user's open connection was not dropped")
	}
	if nc2, err := dial(r.url, kp, tok); err == nil {
		nc2.Close()
		t.Fatal("a revoked user connected again")
	}
}

func TestOversizedPayloadIsRefused(t *testing.T) {
	r := newRig(t, 8, func(c *natsbus.Config) { c.MaxPayload = 2048 })
	kp, tok := edge(t, r.chain)
	nc, err := dial(r.url, kp, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if nc.MaxPayload() != 2048 {
		t.Fatalf("the server advertises %d, not its configured limit", nc.MaxPayload())
	}
	if err := nc.Publish("g.ev.edge.x", make([]byte, 4096)); !errors.Is(err, nats.ErrMaxPayload) {
		t.Fatalf("an oversized message was not refused: %v", err)
	}
}

// A socket that upgrades and never authenticates is closed by the server.
func TestSilentSocketIsClosedAtTheAuthTimeout(t *testing.T) {
	r := newRig(t, 8, nil)
	conn := rawUpgrade(t, r.host, wsproxy.Path)
	defer func() { _ = conn.Close() }()
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil { // nil: the server closed it
		t.Fatalf("a silent socket was still open after the auth timeout: %v", err)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Fatalf("closed after %s, before the auth timeout", took)
	}
}

// Past the cap a connection gets 503 and costs the server nothing; a freed
// slot is usable again.
func TestConnectionFloodPastTheCapIsRefused(t *testing.T) {
	const limit = 3
	r := newRig(t, limit, nil)
	kp, tok := edge(t, r.chain)
	var held []*nats.Conn
	for range limit {
		nc, err := dial(r.url, kp, tok)
		if err != nil {
			t.Fatalf("a connection under the cap was refused: %v", err)
		}
		held = append(held, nc)
	}
	if nc, err := dial(r.url, kp, tok); err == nil {
		nc.Close()
		t.Fatal("a connection past the cap was accepted")
	}
	resp := rawStatus(t, r.host, wsproxy.Path)
	if resp != http.StatusServiceUnavailable {
		t.Fatalf("past the cap the proxy answered %d, not 503", resp)
	}
	held[0].Close()
	var err error
	for range 50 { // the proxy frees the slot when the tunnel ends
		var nc *nats.Conn
		if nc, err = dial(r.url, kp, tok); err == nil {
			nc.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("a freed slot was not usable: %v", err)
	}
	for _, nc := range held[1:] {
		nc.Close()
	}
}

// An edge's leaf reaches the core through /bus/leafnode with its JWT.
func TestLeafConnectsOverTheLeafPath(t *testing.T) {
	r := newRig(t, 8, nil)
	kp, tok := edge(t, r.chain)
	seed, _ := kp.Seed()
	creds, err := jwt.FormatUserConfig(tok, seed)
	if err != nil {
		t.Fatal(err)
	}
	f := t.TempDir() + "/edge.creds"
	if err := writeFile(f, creds); err != nil {
		t.Fatal(err)
	}
	u, _ := parseURL("ws://" + r.host + wsproxy.Path)
	srv, err := server.NewServer(&server.Options{
		Host: "127.0.0.1", Port: -1, NoSigs: true,
		LeafNode: server.LeafNodeOpts{Remotes: []*server.RemoteLeafOpts{{URLs: []*urlT{u}, Credentials: f}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	defer srv.Shutdown()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("leaf server not ready")
	}
	deadline := time.Now().Add(8 * time.Second)
	for srv.NumLeafNodes() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if srv.NumLeafNodes() == 0 {
		t.Fatal("the leaf did not connect through /bus")
	}
}

// rawUpgrade does the WebSocket handshake by hand and returns the open
// socket, which has sent no NATS CONNECT.
func rawUpgrade(t *testing.T, host, path string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET "+path+" HTTP/1.1\r\nHost: "+host+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	return c
}

// rawStatus makes the same request and returns the status line's code.
func rawStatus(t *testing.T, host, path string) int {
	t.Helper()
	c := rawUpgrade(t, host, path)
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, _ := c.Read(buf)
	line := string(buf[:n])
	if len(line) < 12 {
		t.Fatalf("short answer %q", line)
	}
	switch line[9:12] {
	case "503":
		return 503
	case "101":
		return 101
	}
	return 0
}
