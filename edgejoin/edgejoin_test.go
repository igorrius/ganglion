package edgejoin

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/secrets"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Get(_ context.Context, sc secrets.Scope, n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[string(sc)+"/"+n], nil
}

func (s *memStore) Set(_ context.Context, sc secrets.Scope, n, v, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(sc)+"/"+n] = v
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	svc    *Service
	bus    *natsbus.Bus
	store  *memStore
	clk    *clock
	srv    *httptest.Server
	joined chan Edge
	needs  *natsbus.EdgeNeeds
	mu     sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: &memStore{m: map[string]string{}}, clk: &clock{t: time.Now()}, joined: make(chan Edge, 8), needs: &natsbus.EdgeNeeds{}}
	// The chain keeps the real clock: the server compares a revocation's
	// time with the JWT's issue time, which the JWT library takes from the
	// real clock, so a frozen chain clock would revoke nothing issued a
	// second later.
	chain, err := natsbus.LoadChain(context.Background(), r.store, func() bool { return false }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := natsbus.DefaultConfig(t.TempDir())
	cfg.Chain, cfg.WebSocket = chain, true
	b, err := natsbus.Open(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.bus = b
	r.svc = New(Config{Chain: chain, Bus: b, Store: r.store, Now: r.clk.now,
		Needs: func(string) (natsbus.EdgeNeeds, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return *r.needs, nil
		},
		Joined: func(_ context.Context, e Edge) { r.joined <- e }})
	mux := http.NewServeMux()
	log := slog.New(slog.DiscardHandler)
	mux.Handle(JoinPath, r.svc.JoinHandler(log))
	mux.Handle(RenewPath, r.svc.RenewHandler(log))
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// code asks for the code of a new edge as the owner would: card, then Approve.
func (r *rig) code(t *testing.T, name string) string {
	t.Helper()
	p, err := r.svc.Propose(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := r.svc.Approve(context.Background(), name, p.Digest)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) join(code string) (Credentials, error) {
	return Join(context.Background(), r.srv.Client(), r.srv.URL, code)
}

func refusedWith(t *testing.T, err error, status int) {
	t.Helper()
	var re *RefusedError
	if !errors.As(err, &re) || re.Status != status {
		t.Fatalf("err = %v, want a refusal with status %d", err, status)
	}
}

// connect dials the bus's WebSocket entry as the edge.
func (r *rig) connect(c Credentials) (func(), <-chan struct{}, error) {
	return natsbus.DialUser("ws://"+r.bus.WebSocketAddr(), c.Creds, 3*time.Second)
}

func TestJoinGivesAWorkingEdge(t *testing.T) {
	r := newRig(t)
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if creds.Name != "home" || creds.Operator != r.svc.cfg.Chain.OperatorPublicKey() {
		t.Fatalf("answer = %+v", creds)
	}
	if !strings.HasPrefix(creds.BusURL, "ws://127.0.0.1:") || !strings.HasSuffix(creds.BusURL, "/bus") {
		t.Fatalf("bus url = %s", creds.BusURL)
	}
	done, _, err := r.connect(creds)
	if err != nil {
		t.Fatalf("the joined edge could not connect: %v", err)
	}
	done()
	select {
	case e := <-r.joined:
		if e.Name != "home" {
			t.Fatalf("joined %v", e)
		}
	default:
		t.Fatal("the owner was not told of the join")
	}
	// The seed is made on the edge: the JWT's subject is the key the edge
	// made, and the node holds only that public key.
	uc, err := natsbus.ReadCreds(creds.Creds)
	if err != nil {
		t.Fatal(err)
	}
	es, _ := r.svc.Edges(context.Background())
	if len(es) != 1 || es[0].Key != uc.Subject {
		t.Fatalf("edges = %+v", es)
	}
	seedLine := strings.SplitAfter(string(creds.Creds), "SEED-----\n")[1][:58]
	for _, v := range r.store.m {
		if strings.Contains(v, seedLine) {
			t.Fatal("the edge's seed is in the node's store")
		}
	}
}

func mustSeedCreds(t *testing.T, creds []byte) natsbus.UserKey {
	t.Helper()
	kp, err := natsbus.UserKeyFromCreds(creds)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func TestReusedCodeIsRefused(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	if _, err := r.join(c); err != nil {
		t.Fatal(err)
	}
	_, err := r.join(c)
	refusedWith(t, err, http.StatusUnauthorized)
	if es, _ := r.svc.Edges(context.Background()); len(es) != 1 {
		t.Fatalf("%d edges after a reused code", len(es))
	}
}

func TestExpiredCodeIsRefused(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	r.clk.add(CodeTTL + time.Second)
	_, err := r.join(c)
	refusedWith(t, err, http.StatusUnauthorized)
	if es, _ := r.svc.Edges(context.Background()); len(es) != 0 {
		t.Fatal("an expired code made an edge")
	}
}

func TestCodeJustInTimeWorks(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	r.clk.add(CodeTTL - time.Second)
	if _, err := r.join(c); err != nil {
		t.Fatal(err)
	}
}

func TestWrongCodeIsRefusedAndTheRightOneStillWorks(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	for _, bad := range []string{"", "0000-0000-0000-0000-0000-0000-00", strings.ToLower(c)[:10], c + "X"} {
		_, err := r.join(bad)
		if bad == strings.ToLower(c)[:10] {
			refusedWith(t, err, http.StatusUnauthorized)
			continue
		}
		refusedWith(t, err, http.StatusUnauthorized)
	}
	if _, err := r.join(c); err != nil {
		t.Fatalf("the right code failed after wrong ones: %v", err)
	}
}

func TestCodeIsCaseAndDashInsensitive(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	if _, err := r.join(strings.ToLower(strings.ReplaceAll(c, "-", " "))); err != nil {
		t.Fatalf("a code typed in lower case with spaces: %v", err)
	}
}

func TestCodeIsBoundToItsName(t *testing.T) {
	r := newRig(t)
	a, b := r.code(t, "alpha"), r.code(t, "beta")
	ca, err := r.join(a)
	if err != nil || ca.Name != "alpha" {
		t.Fatalf("%v %+v", err, ca)
	}
	cb, err := r.join(b)
	if err != nil || cb.Name != "beta" {
		t.Fatalf("%v %+v", err, cb)
	}
	// A second code for one name replaces the first.
	x := r.code(t, "gamma")
	y := r.code(t, "gamma")
	_, err = r.join(x)
	refusedWith(t, err, http.StatusUnauthorized)
	if _, err := r.join(y); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitRefusesWithoutLooking(t *testing.T) {
	r := newRig(t)
	good := r.code(t, "home")
	for range FailBurst {
		_, err := r.join("WRONG")
		refusedWith(t, err, http.StatusUnauthorized)
	}
	_, err := r.join("WRONG")
	refusedWith(t, err, http.StatusTooManyRequests)
	// Limited: even the right code is not looked at.
	_, err = r.join(good)
	refusedWith(t, err, http.StatusTooManyRequests)
	r.clk.add(FailEvery + time.Second)
	if _, err := r.join(good); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
}

func TestNameCannotBeTakenTwice(t *testing.T) {
	r := newRig(t)
	if _, err := r.join(r.code(t, "home")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Propose(context.Background(), "home"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("a card for a taken name: %v", err)
	}
	for _, name := range []string{"core", "a.b", ">", ""} {
		if _, err := r.svc.Propose(context.Background(), name); err == nil {
			t.Fatalf("a card for %q", name)
		}
	}
}

func TestApproveRefusesAChangedCard(t *testing.T) {
	r := newRig(t)
	p, err := r.svc.Propose(context.Background(), "home")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.needs.Calls = []natsbus.PortRef{{Module: "reader", Port: "read"}}
	r.mu.Unlock()
	if _, _, err := r.svc.Approve(context.Background(), "home", p.Digest); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
}

func TestJoinRefusesBadKeys(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	for _, k := range []string{"", "not a key", r.svc.cfg.Chain.CorePublicKey(), r.svc.cfg.Chain.OperatorPublicKey()} {
		if _, err := r.svc.Join(context.Background(), c, k); !errors.Is(err, ErrBadKey) {
			t.Fatalf("key %q: %v", k, err)
		}
	}
	if _, err := r.join(c); err != nil {
		t.Fatalf("a bad key used up the code: %v", err)
	}
}

func TestRevokedKeyCannotReconnectOrJoinOrRenew(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	done, closed, err := r.connect(creds)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err := r.svc.Revoke(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked edge's connection stayed open")
	}
	// The server's refusal can race the socket's close, which the client
	// then sees as a broken pipe: no try may get in, and one must read the
	// refusal itself.
	refused := false
	for range 10 {
		d2, _, err := r.connect(creds)
		if err == nil {
			d2()
			t.Fatal("a revoked edge reconnected")
		}
		if errors.Is(err, natsbus.ErrUserRefused) {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("no reconnect of the revoked edge was refused by the server for its credentials")
	}
	if es, _ := r.svc.Edges(ctx); len(es) != 0 {
		t.Fatalf("edges = %+v", es)
	}
	// The same key cannot renew, nor join again with a new code.
	_, err = Renew(ctx, r.srv.Client(), r.srv.URL, creds.Creds, r.clk.now())
	refusedWith(t, err, http.StatusForbidden)
	pub, _ := mustSeedCreds(t, creds.Creds).PublicKey()
	c := r.code(t, "home")
	if _, err := r.svc.Join(ctx, c, pub); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a revoked key joined: %v", err)
	}
	// A second revoke finds nothing.
	if err := r.svc.Revoke(ctx, "home"); !errors.Is(err, ErrUnknownEdge) {
		t.Fatalf("err = %v", err)
	}
	// The name is free for a new machine.
	if _, err := r.join(c); err != nil {
		t.Fatalf("the name stayed taken: %v", err)
	}
}

func TestRenewIssuesCurrentPermissions(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := natsbus.ReadCreds(creds.Creds)
	r.mu.Lock()
	r.needs.Calls = []natsbus.PortRef{{Module: "reader", Port: "read"}}
	r.mu.Unlock()
	r.clk.add(RenewEvery)
	got, err := Renew(ctx, r.srv.Client(), r.srv.URL, creds.Creds, r.clk.now())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := natsbus.ReadCreds(got.Creds)
	if len(after.Permissions.Publish) != len(before.Permissions.Publish)+1 {
		t.Fatalf("publish before %v after %v", before.Permissions.Publish, after.Permissions.Publish)
	}
	done, _, err := r.connect(got)
	if err != nil {
		t.Fatalf("the renewed JWT does not connect: %v", err)
	}
	done()
	es, _ := r.svc.Edges(ctx)
	if es[0].Digest != Digest(after.Permissions) {
		t.Fatal("the list keeps the old digest")
	}
}

func TestRenewRefusals(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	kp := mustSeedCreds(t, creds.Creds)
	pub, _ := kp.PublicKey()
	sign := func(k natsbus.UserKey, at int64) string {
		sig, _ := k.Sign(RenewMessage(pub, at))
		return base64.StdEncoding.EncodeToString(sig)
	}
	other, _ := natsbus.NewUserKey()
	stranger, _ := natsbus.NewUserKey()
	spub, _ := stranger.PublicKey()
	type req struct {
		pub string
		at  int64
		sig string
	}
	cases := map[string]func(now int64) req{
		"another key signed":   func(n int64) req { return req{pub, n, sign(other, n)} },
		"a stale time":         func(n int64) req { return req{pub, n - 120, sign(kp, n-120)} },
		"a time in the future": func(n int64) req { return req{pub, n + 120, sign(kp, n+120)} },
		"a time not signed":    func(n int64) req { return req{pub, n, sign(kp, n-1)} },
		"not base64":           func(n int64) req { return req{pub, n, "%%%"} },
		"an unknown edge": func(n int64) req {
			sig, _ := stranger.Sign(RenewMessage(spub, n))
			return req{spub, n, base64.StdEncoding.EncodeToString(sig)}
		},
	}
	for name, mk := range cases {
		c := mk(r.clk.now().Unix())
		if _, err := r.svc.Renew(ctx, c.pub, c.at, c.sig); err == nil {
			t.Fatalf("%s: renewed", name)
		}
		r.clk.add(FailEvery) // the limiter is not under test here
	}
	now := r.clk.now().Unix()
	if _, err := r.svc.Renew(ctx, pub, now, sign(kp, now)); err != nil {
		t.Fatalf("the right request failed: %v", err)
	}
}

func TestHandlerShape(t *testing.T) {
	r := newRig(t)
	get, err := http.Get(r.srv.URL + JoinPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", get.StatusCode)
	}
	for _, body := range []string{`{"code":"x","public_key":"y","extra":1}`, `{`, strings.Repeat("a", 5000), `{"code":"x"} {}`} {
		resp, err := http.Post(r.srv.URL+JoinPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%.20q = %d", body, resp.StatusCode)
		}
	}
}

func TestEndpointNeedsHTTPS(t *testing.T) {
	for _, bad := range []string{"http://example.com", "ftp://example.com", "https://u:p@example.com", "example.com", "https://example.com?x=1"} {
		if _, err := Endpoint(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	for _, ok := range []string{"https://example.com", "https://example.com/", "http://127.0.0.1:8080"} {
		if _, err := Endpoint(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}

func TestCodesAreNotKeptInTheClear(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	plain := strings.ReplaceAll(c, "-", "")
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	if len(r.svc.codes) != 1 {
		t.Fatal("no code kept")
	}
	for k := range r.svc.codes {
		if strings.Contains(string(k[:]), plain) {
			t.Fatal("the code is kept as it is")
		}
	}
}

// A stranger flooding /edge/renew with bad requests does not hold back a
// joined edge's renewal, nor the owner's join.
func TestBadRenewFloodDoesNotStopRenewOrJoin(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := natsbus.NewUserKey()
	spub, _ := stranger.PublicKey()
	for range 50 {
		now := r.clk.now().Unix()
		_, err := r.svc.Renew(ctx, spub, now, base64.StdEncoding.EncodeToString([]byte("not a signature")))
		if !errors.Is(err, ErrBadSignature) {
			t.Fatalf("a bad renewal: %v", err)
		}
	}
	if _, err := Renew(ctx, r.srv.Client(), r.srv.URL, creds.Creds, r.clk.now()); err != nil {
		t.Fatalf("a valid renewal after a flood: %v", err)
	}
	if _, err := r.join(r.code(t, "second")); err != nil {
		t.Fatalf("a join after a flood of renewals: %v", err)
	}
}

// Parallel attempts never pass on one token.
func TestLimiterHoldsUnderParallelAttempts(t *testing.T) {
	r := newRig(t)
	for range FailBurst - 1 {
		_, _ = r.svc.Join(context.Background(), "WRONG", "")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	looked := 0
	for range 20 {
		wg.Go(func() {
			if _, err := r.svc.Join(context.Background(), "WRONG", ""); !errors.Is(err, ErrLimited) {
				mu.Lock()
				looked++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if looked != 1 {
		t.Fatalf("%d attempts were looked at on one token", looked)
	}
}

// The edge gets the permissions the owner read on the card, or nothing: a
// change of the links between Approve and the join refuses it.
func TestJoinRefusesPermissionsTheCardDidNotShow(t *testing.T) {
	r := newRig(t)
	c := r.code(t, "home")
	r.mu.Lock()
	r.needs.Calls = []natsbus.PortRef{{Module: "reader", Port: "read"}}
	r.mu.Unlock()
	_, err := r.join(c)
	refusedWith(t, err, http.StatusConflict)
	if es, _ := r.svc.Edges(context.Background()); len(es) != 0 {
		t.Fatalf("an edge joined with permissions not on its card: %+v", es)
	}
}

// One key is one edge.
func TestJoinRefusesAKeyAJoinedEdgeHolds(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	creds, err := r.join(r.code(t, "home"))
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := mustSeedCreds(t, creds.Creds).PublicKey()
	c := r.code(t, "other")
	if _, err := r.svc.Join(ctx, c, pub); !errors.Is(err, ErrKeyTaken) {
		t.Fatalf("a second name under one key: %v", err)
	}
	if _, err := r.join(c); err != nil {
		t.Fatalf("the refused key used up the code: %v", err)
	}
	if es, _ := r.svc.Edges(ctx); len(es) != 2 {
		t.Fatalf("edges = %+v", es)
	}
}
