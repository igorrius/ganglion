package edge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/secrets"
)

func TestNodeURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://example.com/bus":      "https://example.com",
		"wss://example.com:8443/bus": "https://example.com:8443",
		"wss://example.com/x/bus":    "https://example.com/x",
		"ws://127.0.0.1:9/bus":       "http://127.0.0.1:9",
	} {
		got, err := NodeURL(in)
		if err != nil || got != want {
			t.Errorf("NodeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"nats-leaf://example.com:7422", "tls://example.com", "wss:///bus", "::"} {
		if got, err := NodeURL(bad); err == nil {
			t.Errorf("NodeURL(%q) = %q, want an error", bad, got)
		}
	}
}

// fakeIssuer answers /edge/renew with a JWT for the request's key that
// carries the permissions in perms at that moment.
type fakeIssuer struct {
	mu     sync.Mutex
	chain  *natsbus.Chain
	perms  []string
	status int // 0 is 200
	calls  int
}

func (f *fakeIssuer) jwtFor(t *testing.T, pub string, perms []string, ttl time.Duration) string {
	t.Helper()
	tok, err := f.chain.IssueUser("home", pub, natsbus.UserPermissions{Publish: perms, Subscribe: []string{"x"}}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *fakeIssuer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if f.status != 0 {
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no"})
			return
		}
		var q edgejoin.RenewRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		_, _ = base64.StdEncoding.DecodeString(q.Signature)
		_ = json.NewEncoder(w).Encode(edgejoin.Answer{Name: "home", JWT: f.jwtFor(t, q.PublicKey, f.perms, time.Hour), Expires: time.Now().Add(time.Hour)})
	})
}

type renewRig struct {
	r      *Renewer
	file   string
	iss    *fakeIssuer
	logs   *bytes.Buffer
	ticks  chan time.Time
	redial int
	mu     sync.Mutex
}

func newRenewRig(t *testing.T, oldPerms, newPerms []string, oldTTL time.Duration) *renewRig {
	t.Helper()
	chain, err := natsbus.LoadChain(context.Background(), &mapStore{m: map[string]string{}}, func() bool { return false }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	iss := &fakeIssuer{chain: chain, perms: newPerms}
	srv := httptest.NewServer(iss.handler(t))
	t.Cleanup(srv.Close)
	kp, _ := natsbus.NewUserKey()
	pub, _ := kp.PublicKey()
	creds, err := kp.Creds(iss.jwtFor(t, pub, oldPerms, oldTTL))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "edge.creds")
	if err := os.WriteFile(file, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	g := &renewRig{file: file, iss: iss, logs: &bytes.Buffer{}, ticks: make(chan time.Time)}
	r := NewRenewer(srv.URL, file, func() error { g.mu.Lock(); g.redial++; g.mu.Unlock(); return nil }, slog.New(slog.NewTextHandler(&syncBuf{b: g.logs, mu: &g.mu}, nil)))
	r.Every = time.Hour
	r.Ticker = func(time.Duration) (<-chan time.Time, func()) { return g.ticks, func() {} }
	g.r = r
	return g
}

type syncBuf struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

func (g *renewRig) run(_ *testing.T) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = g.r.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func (g *renewRig) tick(t *testing.T) {
	t.Helper()
	g.ticks <- time.Now()
	g.ticks <- time.Now() // returns once the first is handled; it renews again, which is harmless
}

func (g *renewRig) state() (redials int, logs string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.redial, g.logs.String()
}

func (g *renewRig) perms(t *testing.T) []string {
	raw, err := os.ReadFile(g.file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := natsbus.ReadCreds(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c.Permissions.Publish
}

func TestRenewerWritesTheNewJWTAndRedialsOnlyWhenPermissionsChange(t *testing.T) {
	g := newRenewRig(t, []string{"a"}, []string{"a"}, 20*time.Hour)
	defer g.run(t)()
	before, _ := os.ReadFile(g.file)
	g.tick(t)
	if n, _ := g.state(); n != 0 {
		t.Fatalf("redialled %d times for the same permissions", n)
	}
	after, _ := os.ReadFile(g.file)
	if bytes.Equal(before, after) {
		t.Fatal("the JWT was not renewed")
	}
	if st, _ := os.Stat(g.file); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}

	g.iss.mu.Lock()
	g.iss.perms = []string{"a", "b"}
	g.iss.mu.Unlock()
	g.tick(t)
	if n, _ := g.state(); n != 1 {
		t.Fatalf("redialled %d times, want 1", n)
	}
	if got := g.perms(t); len(got) != 2 {
		t.Fatalf("permissions in the file: %v", got)
	}
}

func TestRenewerRenewsAtStartWhenTheJWTIsAboutToExpire(t *testing.T) {
	g := newRenewRig(t, []string{"a"}, []string{"a"}, time.Hour) // less than 2 periods
	stop := g.run(t)
	defer stop()
	waitUntil(t, func() bool { g.iss.mu.Lock(); defer g.iss.mu.Unlock(); return g.iss.calls == 1 })

	fresh := newRenewRig(t, []string{"a"}, []string{"a"}, 20*time.Hour)
	defer fresh.run(t)()
	time.Sleep(50 * time.Millisecond)
	fresh.iss.mu.Lock()
	defer fresh.iss.mu.Unlock()
	if fresh.iss.calls != 0 {
		t.Fatalf("renewed at start with a fresh JWT (%d calls)", fresh.iss.calls)
	}
}

func TestRenewerLogsRefusalsAtTheRightLevelAndKeepsGoing(t *testing.T) {
	g := newRenewRig(t, []string{"a"}, []string{"a"}, 20*time.Hour)
	defer g.run(t)()
	before, _ := os.ReadFile(g.file)

	g.iss.mu.Lock()
	g.iss.status = http.StatusForbidden
	g.iss.mu.Unlock()
	g.tick(t)
	if _, logs := g.state(); !strings.Contains(logs, "level=ERROR") {
		t.Fatalf("a 403 is not an error:\n%s", logs)
	}

	g.iss.mu.Lock()
	g.iss.status = http.StatusInternalServerError
	g.iss.mu.Unlock()
	g.tick(t)
	if _, logs := g.state(); !strings.Contains(logs, "level=WARN") {
		t.Fatalf("a 500 is not a warning:\n%s", logs)
	}
	if after, _ := os.ReadFile(g.file); !bytes.Equal(before, after) {
		t.Fatal("a refusal changed the file")
	}

	g.iss.mu.Lock()
	g.iss.status = 0
	g.iss.mu.Unlock()
	g.tick(t)
	if after, _ := os.ReadFile(g.file); bytes.Equal(before, after) {
		t.Fatal("the edge did not renew after the node came back")
	}
}

func TestRenewerSurvivesAnUnreachableNode(t *testing.T) {
	g := newRenewRig(t, []string{"a"}, []string{"a"}, 20*time.Hour)
	g.r.Node = "http://127.0.0.1:1"
	defer g.run(t)()
	g.tick(t)
	if _, logs := g.state(); !strings.Contains(logs, "level=WARN") {
		t.Fatalf("no warning:\n%s", logs)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not reached")
}

// mapStore is a secret store in memory for the test chain.
type mapStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *mapStore) Get(_ context.Context, sc secrets.Scope, n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[string(sc)+"/"+n], nil
}

func (s *mapStore) Set(_ context.Context, sc secrets.Scope, n, v, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(sc)+"/"+n] = v
	return nil
}
