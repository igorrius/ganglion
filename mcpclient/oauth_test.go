package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
)

// page is the intake page the node registers as its redirect address.
const page = "https://example.com/intake/v1/"

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// oauthRig is a protected MCP server at <origin>/mcp, its authorization
// server, a store and the node's OAuth.
type oauthRig struct {
	as    *mcpclienttest.AuthServer
	mcp   *mcpclienttest.Server
	url   string
	store *mcpclienttest.MemStore
	clock *clock
	oauth *mcpclient.OAuth
	tools []json.RawMessage
	logs  *safeBuf
}

// newOAuthRig starts the servers; the resource's metadata is the
// path-suffixed document, named by the challenge.
func newOAuthRig(t *testing.T, ao mcpclienttest.AuthOptions) *oauthRig {
	t.Helper()
	r := &oauthRig{as: mcpclienttest.NewAuthServer(t, ao), store: mcpclienttest.NewMemStore(), clock: newClock(),
		tools: []json.RawMessage{mcpclienttest.Tool("search", "Search.", "", true)}, logs: &safeBuf{}}
	r.mcp = mcpclienttest.NewTLS(t, mcpclienttest.Options{Tools: r.tools, Bearer: r.as.Valid})
	r.url = r.mcp.URL + "/mcp"
	r.mcp.Update(func(o *mcpclienttest.Options) {
		o.Challenge = `Bearer error="invalid_token", resource_metadata="` + r.mcp.URL + `/.well-known/oauth-protected-resource/mcp", scope="read"`
		o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": r.resourceDoc(r.url, r.as.URL)}
	})
	r.oauth = r.newOAuth()
	return r
}

func (r *oauthRig) newOAuth() *mcpclient.OAuth {
	return mcpclient.NewOAuth(r.mcp.Client(), r.store, page, mcpclient.WithOAuthClock(r.clock.now),
		mcpclient.WithOAuthLogger(slogTo(r.logs)))
}

func (r *oauthRig) resourceDoc(resource string, issuers ...string) string {
	b, _ := json.Marshal(map[string]any{"resource": resource, "authorization_servers": issuers,
		"scopes_supported": []string{"read", "write"}})
	return string(b)
}

// signIn begins a sign-in, has the fake sign the admin in, and completes
// it with the answer the page would show.
func (r *oauthRig) signIn(t *testing.T, state string) {
	t.Helper()
	si, err := r.oauth.Begin(context.Background(), "shop", r.url, state)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	res, err := r.as.SignIn(si.URL)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if err := r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: res.Issuer}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func (r *oauthRig) client() *mcpclient.Client {
	return mcpclient.NewClient("shop", r.url, r.oauth.Auth("shop"), testTimeout, mcpclient.WithHTTPClient(r.mcp.Client()))
}

// The whole flow (R-MCP-3, R-MCP-5): 401 → the path-suffixed resource
// metadata → the issuer's metadata → registration with the intake page as
// the only redirect → a link with PKCE S256, state and resource → the code
// exchanged with the verifier and the resource → the server answers with
// the token, which the store holds.
func TestOAuthSignInFlow(t *testing.T) {
	rec := telemetrytest.Install(t)
	r := newOAuthRig(t, mcpclienttest.AuthOptions{IssParameter: true})
	si, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	link, _ := url.Parse(si.URL)
	q := link.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") != "state-1" ||
		q.Get("resource") != r.url || q.Get("redirect_uri") != page || q.Get("scope") != "read" || q.Get("code_verifier") != "" {
		t.Fatalf("link query = %v", q)
	}
	if si.SignInHost != "127.0.0.1" || si.ServerHost != "127.0.0.1" || !si.Expires.Equal(r.clock.now().Add(10*time.Minute)) {
		t.Fatalf("sign-in = %+v", si)
	}
	var reg struct {
		RedirectURIs []string `json:"redirect_uris"`
		Method       string   `json:"token_endpoint_auth_method"`
	}
	if regs := r.as.Requests("/register"); len(regs) != 1 || json.Unmarshal(regs[0].Body, &reg) != nil ||
		len(reg.RedirectURIs) != 1 || reg.RedirectURIs[0] != page || reg.Method != "none" {
		t.Fatalf("registration = %+v", reg)
	}
	res, err := r.as.SignIn(si.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: res.Issuer}); err != nil {
		t.Fatal(err)
	}
	tok := r.as.Requests("/token")[0].Form
	if tok.Get("code_verifier") == "" || tok.Get("resource") != r.url || tok.Get("grant_type") != "authorization_code" {
		t.Fatalf("token request = %v", tok)
	}
	c := r.client()
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l, err := c.ListTools(context.Background()); err != nil || len(l.Tools) != 1 {
		t.Fatalf("ListTools = %+v, %v", l, err)
	}
	if v := r.store.Values("shop"); len(v) != 1 || !strings.Contains(v["oauth"], "client-") || !strings.Contains(v["oauth"], "access-") {
		t.Fatalf("store = %v", v)
	}
	// A line works once.
	if err := r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: res.Issuer}); !errors.Is(err, mcpclient.ErrUnknownSignIn) {
		t.Fatalf("second Complete = %v", err)
	}
	for _, step := range []string{"discover", "register", "exchange"} {
		if got := rec.Value(t, "ganglion.mcpclient.oauth", map[string]string{"step": step, "outcome": "ok"}); got != 1 {
			t.Errorf("oauth %s ok = %v", step, got)
		}
	}
}

// Without a challenge that names it, the bare well-known document is found
// after the path-suffixed one is missing (RFC 9728, 3.1).
func TestOAuthBareResourceDocument(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.mcp.Update(func(o *mcpclienttest.Options) {
		o.Challenge = `Bearer realm="mcp"`
		o.Documents = map[string]string{"/.well-known/oauth-protected-resource": r.resourceDoc(r.mcp.URL, r.as.URL)}
	})
	r.signIn(t, "state-1")
	if err := r.client().Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.as.Requests("/token")[0].Form.Get("resource"); got != r.mcp.URL {
		t.Fatalf("resource = %q, want the document's", got)
	}
}

// Metadata the node refuses: every address is checked, a document on
// another host than the server's is not read, and what the documents say
// must match what was asked (R-MCP-3).
func TestOAuthRefusedMetadata(t *testing.T) {
	tests := []struct {
		name  string
		bend  func(r *oauthRig)
		auth  func(*mcpclienttest.AuthOptions)
		check func(t *testing.T, r *oauthRig)
	}{
		{name: "metadata on a foreign host", bend: func(r *oauthRig) {
			r.mcp.Update(func(o *mcpclienttest.Options) {
				o.Challenge = `Bearer resource_metadata="` + r.as.URL + `/.well-known/oauth-protected-resource/mcp"`
			})
		}, check: func(t *testing.T, r *oauthRig) {
			if n := len(r.as.Requests("")); n != 0 {
				t.Fatalf("the foreign host got %d requests", n)
			}
		}},
		{name: "metadata over http", bend: func(r *oauthRig) {
			r.mcp.Update(func(o *mcpclienttest.Options) {
				o.Challenge = `Bearer resource_metadata="http://` + strings.TrimPrefix(r.mcp.URL, "https://") + `/.well-known/oauth-protected-resource/mcp"`
			})
		}},
		{name: "an authorization server over http", bend: func(r *oauthRig) {
			r.mcp.Update(func(o *mcpclienttest.Options) {
				o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": r.resourceDoc(r.url, "http://"+strings.TrimPrefix(r.as.URL, "https://"))}
			})
		}},
		{name: "a document about another resource", bend: func(r *oauthRig) {
			r.mcp.Update(func(o *mcpclienttest.Options) {
				o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": r.resourceDoc(r.as.URL+"/mcp", r.as.URL)}
			})
		}},
		{name: "a resource on a sibling path", bend: func(r *oauthRig) {
			r.mcp.Update(func(o *mcpclienttest.Options) {
				o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": r.resourceDoc(r.mcp.URL+"/mc", r.as.URL)}
			})
		}},
		{name: "another issuer", auth: func(o *mcpclienttest.AuthOptions) {
			o.Metadata = func(doc map[string]any) { doc["issuer"] = "https://evil.example.com" }
		}},
		{name: "a token endpoint over http", auth: func(o *mcpclienttest.AuthOptions) {
			o.Metadata = func(doc map[string]any) { doc["token_endpoint"] = "http://evil.example.com/token" }
		}},
		{name: "no PKCE S256", auth: func(o *mcpclienttest.AuthOptions) {
			o.Metadata = func(doc map[string]any) { doc["code_challenge_methods_supported"] = []string{"plain"} }
		}},
		{name: "no registration", auth: func(o *mcpclienttest.AuthOptions) {
			o.Metadata = func(doc map[string]any) { delete(doc, "registration_endpoint") }
		}},
		{name: "a redirect of the metadata", auth: func(o *mcpclienttest.AuthOptions) {
			o.Intercept = func(w http.ResponseWriter, req *http.Request) bool {
				if req.URL.Path != "/.well-known/oauth-authorization-server" {
					return false
				}
				http.Redirect(w, req, "https://evil.example.com/meta", http.StatusFound)
				return true
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ao mcpclienttest.AuthOptions
			if tt.auth != nil {
				tt.auth(&ao)
			}
			r := newOAuthRig(t, ao)
			if tt.bend != nil {
				tt.bend(r)
			}
			_, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
			if !errors.Is(err, mcpclient.ErrMetadata) {
				t.Fatalf("Begin = %v, want ErrMetadata", err)
			}
			if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "evil") {
				t.Fatalf("the error names an address: %v", err)
			}
			if n := len(r.as.Requests("/register")); n != 0 {
				t.Fatalf("registered %d times", n)
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

// A server that answers without a credential needs no sign-in.
func TestOAuthNoSignIn(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.mcp.Update(func(o *mcpclienttest.Options) { o.Bearer = nil })
	if _, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1"); !errors.Is(err, mcpclient.ErrNoSignIn) {
		t.Fatalf("Begin = %v", err)
	}
}

// A registration that changed the redirect address is refused.
func TestOAuthRegistrationRefused(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{Registered: func(doc map[string]any) {
		doc["redirect_uris"] = []string{"https://evil.example.com/cb"}
	}})
	if _, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1"); !errors.Is(err, mcpclient.ErrRegistration) {
		t.Fatalf("Begin = %v", err)
	}
	if v := r.store.Values("shop"); len(v) != 0 {
		t.Fatalf("stored %v", v)
	}
}

// A stored registration that fits is used again: one registration for two
// sign-ins, also after a restart.
func TestOAuthReusesRegistration(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	r.oauth = r.newOAuth()
	r.signIn(t, "state-2")
	if n := len(r.as.Requests("/register")); n != 1 {
		t.Fatalf("registered %d times", n)
	}
}

// RFC 9207: when the metadata says `iss` comes, an answer without it, or
// with another, is refused, and the sign-in is closed.
func TestOAuthIssuerChecked(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{IssParameter: true})
	for _, iss := range []string{"", "https://evil.example.com"} {
		si, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.as.SignIn(si.URL)
		if err != nil {
			t.Fatal(err)
		}
		err = r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: iss})
		if !errors.Is(err, mcpclient.ErrIssuer) || strings.Contains(err.Error(), res.Code) {
			t.Fatalf("iss %q: Complete = %v", iss, err)
		}
		if n := len(r.as.Requests("/token")); n != 0 {
			t.Fatalf("the code was exchanged")
		}
		if err := r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: res.Issuer}); !errors.Is(err, mcpclient.ErrUnknownSignIn) {
			t.Fatalf("the sign-in stayed open: %v", err)
		}
	}
}

// A code the server does not take is ErrExchange, and the error holds no
// code.
func TestOAuthBadCode(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	si, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	err = r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: "state-1", Code: "forged:code"})
	if !errors.Is(err, mcpclient.ErrExchange) || strings.Contains(err.Error(), "forged") || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("Complete = %v", err)
	}
	if si.URL == "" {
		t.Fatal("no link")
	}
}

// A sign-in that waited past its time is gone.
func TestOAuthSignInExpires(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	si, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := r.as.SignIn(si.URL)
	r.clock.advance(10 * time.Minute)
	if err := r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code}); !errors.Is(err, mcpclient.ErrUnknownSignIn) {
		t.Fatalf("Complete = %v", err)
	}
}

// An expired token is refreshed before the request, with the resource
// (RFC 8707), and the new one is stored.
func TestOAuthRefreshesAnExpiredToken(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{ExpiresIn: 3600})
	r.signIn(t, "state-1")
	before := r.store.Values("shop")["oauth"]
	r.clock.advance(time.Hour)
	r.as.ExpireAccess()
	if err := r.client().Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := r.as.Requests("/token")
	last := reqs[len(reqs)-1].Form
	if len(reqs) != 2 || last.Get("grant_type") != "refresh_token" || last.Get("resource") != r.url {
		t.Fatalf("token requests = %+v", reqs)
	}
	if after := r.store.Values("shop")["oauth"]; after == before || !strings.Contains(after, "refresh-") {
		t.Fatalf("store not updated: %s", after)
	}
}

// A token the server refuses is refreshed once; one that came from that
// refresh and is refused again means a new sign-in.
func TestOAuthRefusedTokenRefreshedOnce(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	c := r.client()
	r.as.ExpireAccess()
	if err := c.Initialize(context.Background()); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("Initialize = %v", err)
	}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("after the refresh: %v", err)
	}
	r.mcp.Update(func(o *mcpclienttest.Options) { o.Bearer = func(string) bool { return false } })
	if err := c.Initialize(context.Background()); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("Initialize = %v", err)
	}
	if err := c.Initialize(context.Background()); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("Initialize = %v", err)
	}
	refusedBefore := r.mcp.Refused()
	if err := c.Initialize(context.Background()); !errors.Is(err, mcpclient.ErrNeedsSignIn) {
		t.Fatalf("Initialize = %v, want ErrNeedsSignIn", err)
	}
	if r.mcp.Refused() != refusedBefore {
		t.Fatal("a token was sent after the server refused the refreshed one")
	}
}

// A failed refresh (R-MCP-6): the server needs a new sign-in, the hub says
// so once, and a new sign-in brings it back.
func TestOAuthFailedRefreshNeedsSignIn(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{ExpiresIn: 60, RefuseRefresh: true})
	r.signIn(t, "state-1")
	rig := newRig(t, mcpclient.WithHubHTTPClient(r.mcp.Client()))
	cfg := mcpclient.Server{Name: "shop", URL: r.url, Auth: r.oauth.Auth("shop"), Timeout: testTimeout,
		Policies: approve(tool.PolicyAuto, "search"), Fingerprint: mcpclient.Fingerprint(r.tools)}
	rig.hub.Set([]mcpclient.Server{cfg})
	rig.await(t, "shop", mcpclient.StateConnected)

	r.clock.advance(time.Hour)
	_, err := call(handler(t, rig.hub, "shop.search"), `{}`)
	if err == nil {
		t.Fatal("the call ran without a token")
	}
	rig.await(t, "shop", mcpclient.StateUnauthorized)
	for range 3 {
		rig.waits.release(t)
	}
	if _, _, err := r.oauth.Auth("shop").Header(context.Background()); !errors.Is(err, mcpclient.ErrNeedsSignIn) {
		t.Fatalf("Header = %v", err)
	}
	if n := len(r.as.Requests("/token")); n != 2 {
		t.Fatalf("token requests = %d, want the exchange and one refused refresh", n)
	}
	if !strings.Contains(r.logs.String(), "needs a new sign-in") {
		t.Fatalf("logs = %s", r.logs.String())
	}
	r.as.Update(func(o *mcpclienttest.AuthOptions) { o.RefuseRefresh = false })
	r.signIn(t, "state-2")
	rig.waits.release(t)
	rig.await(t, "shop", mcpclient.StateConnected)
	for {
		select {
		case e := <-rig.events:
			if e.state == mcpclient.StateUnauthorized {
				t.Fatalf("unauthorized announced again: %+v", e)
			}
			continue
		default:
		}
		break
	}
}

// Disconnect revokes the refresh and the access token (RFC 7009) and
// removes the stored sign-in; the server then needs a new one.
func TestOAuthDisconnectRevokes(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	if _, _, err := r.oauth.Auth("shop").Header(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.oauth.Disconnect(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	revs := r.as.Requests("/revoke")
	if len(revs) != 2 || revs[0].Form.Get("token_type_hint") != "refresh_token" || revs[1].Form.Get("token_type_hint") != "access_token" ||
		revs[0].Form.Get("client_id") == "" {
		t.Fatalf("revocations = %+v", revs)
	}
	if v := r.store.Values("shop"); len(v) != 0 {
		t.Fatalf("store = %v", v)
	}
	if _, _, err := r.oauth.Auth("shop").Header(context.Background()); !errors.Is(err, mcpclient.ErrNeedsSignIn) {
		t.Fatalf("Header = %v", err)
	}
	if err := r.client().Initialize(context.Background()); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("Initialize = %v", err)
	}
}

// A failed revocation is logged and the sign-in is still removed.
func TestOAuthDisconnectWhenRevocationFails(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	r.as.Update(func(o *mcpclienttest.AuthOptions) {
		o.Intercept = func(w http.ResponseWriter, req *http.Request) bool {
			if req.URL.Path != "/revoke" {
				return false
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
	})
	if err := r.oauth.Disconnect(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if v := r.store.Values("shop"); len(v) != 0 {
		t.Fatalf("store = %v", v)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "not revoked") || strings.Contains(logs, "refresh-") || strings.Contains(logs, "access-") {
		t.Fatalf("logs = %s", logs)
	}
}

// A refresh the sign-in server cannot answer leaves the server
// unreachable, not signed out, and the next try refreshes.
func TestOAuthRefreshUnreachableIsNotSignOut(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{ExpiresIn: 60})
	r.signIn(t, "state-1")
	r.clock.advance(time.Hour)
	r.as.Update(func(o *mcpclienttest.AuthOptions) {
		o.Intercept = func(w http.ResponseWriter, req *http.Request) bool {
			if req.URL.Path != "/token" {
				return false
			}
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
	})
	err := r.client().Initialize(context.Background())
	if !errors.Is(err, mcpclient.ErrUnreachable) || errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("Initialize = %v", err)
	}
	r.as.Update(func(o *mcpclienttest.AuthOptions) { o.Intercept = nil })
	if err := r.client().Initialize(context.Background()); err != nil {
		t.Fatalf("after the server came back: %v", err)
	}
}

// The token is never sent to another origin: a cross-origin redirect of
// the server's GET goes without it (R-MCP-5).
func TestOAuthTokenStaysOnTheServersOrigin(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	var mu sync.Mutex
	var seen []string
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(other.Close)
	target, _ := url.Parse(r.mcp.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = r.mcp.Client().Transport
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			http.Redirect(w, req, other.URL+"/stream", http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(front.Close)
	c := mcpclient.NewClient("shop", front.URL+"/mcp", r.oauth.Auth("shop"), testTimeout, mcpclient.WithHTTPClient(front.Client()))
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Listen(context.Background()); !errors.Is(err, mcpclient.ErrNoStream) {
		t.Fatalf("Listen = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("the other origin saw %q", seen)
	}
}

func slogTo(w *safeBuf) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// An abandoned sign-in at another issuer leaves the stored sign-in as it
// was: after a restart, the stored refresh token still goes only to the
// issuer and the client it came from.
func TestOAuthAbandonedSignInKeepsTheStoredIssuer(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{ExpiresIn: 3600})
	r.signIn(t, "state-1")
	stored := r.store.Values("shop")["oauth"]
	other := mcpclienttest.NewAuthServer(t, mcpclienttest.AuthOptions{})
	r.mcp.Update(func(o *mcpclienttest.Options) {
		o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": r.resourceDoc(r.url, other.URL)}
	})
	if _, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-2"); err != nil {
		t.Fatal(err)
	}
	if got := r.store.Values("shop")["oauth"]; got != stored {
		t.Fatalf("a sign-in that did not complete changed the stored one")
	}
	r.oauth = r.newOAuth() // a restart
	r.clock.advance(2 * time.Hour)
	if _, _, err := r.oauth.Auth("shop").Header(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(other.Requests("/token")); n != 0 {
		t.Fatalf("the other issuer got %d token requests", n)
	}
	reqs := r.as.Requests("/token")
	if last := reqs[len(reqs)-1].Form; last.Get("grant_type") != "refresh_token" {
		t.Fatalf("the refresh did not go to its own issuer: %v", last)
	}
}

// A late 401 for a token already replaced changes nothing: the request
// was in flight while another refreshed.
func TestOAuthLateRejectionOfAnOldToken(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	r.signIn(t, "state-1")
	auth := r.oauth.Auth("shop")
	_, t1, err := auth.Header(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	auth.Rejected(context.Background(), t1) // R1's 401
	_, t2, err := auth.Header(context.Background())
	if err != nil || t2 == t1 {
		t.Fatalf("after the refresh: %q, %v", t2, err)
	}
	auth.Rejected(context.Background(), t1) // R2's late 401
	_, t3, err := auth.Header(context.Background())
	if err != nil || t3 != t2 {
		t.Fatalf("a late refusal of the old token changed the new one: %q, %v", t3, err)
	}
	if n := len(r.as.Requests("/token")); n != 2 {
		t.Fatalf("token requests = %d, want the exchange and one refresh", n)
	}
}

// A refreshed token that cannot be stored is still held, so the rotated
// refresh token is used for the next refresh.
func TestOAuthRotatedTokenHeldWhenTheStoreFails(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{ExpiresIn: 3600})
	r.signIn(t, "state-1")
	auth := r.oauth.Auth("shop")
	r.store.SetFail(errors.New("store locked"))
	for i := range 2 {
		r.clock.advance(2 * time.Hour)
		_, v, err := auth.Header(context.Background())
		if err != nil {
			t.Fatalf("refresh %d: %v", i+1, err)
		}
		if !r.as.Valid(strings.TrimPrefix(v, "Bearer ")) {
			t.Fatalf("refresh %d: a dead token", i+1)
		}
	}
	if !strings.Contains(r.logs.String(), "not stored") {
		t.Fatalf("logs = %s", r.logs.String())
	}
}

// A Disconnect while the code is being exchanged wins: the sign-in keeps
// nothing, revokes the tokens it got, and the server stays signed out.
func TestOAuthDisconnectDuringExchange(t *testing.T) {
	r := newOAuthRig(t, mcpclienttest.AuthOptions{})
	si, err := r.oauth.Begin(context.Background(), "shop", r.url, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.as.SignIn(si.URL)
	if err != nil {
		t.Fatal(err)
	}
	arrived, release := make(chan struct{}), make(chan struct{})
	r.as.Update(func(o *mcpclienttest.AuthOptions) {
		o.Intercept = func(_ http.ResponseWriter, req *http.Request) bool {
			if req.URL.Path == "/token" {
				close(arrived)
				<-release
			}
			return false
		}
	})
	done := make(chan error, 1)
	go func() {
		done <- r.oauth.Complete(context.Background(), intake.OAuthAnswer{ID: res.State, Code: res.Code, Issuer: res.Issuer})
	}()
	<-arrived
	if err := r.oauth.Disconnect(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	r.as.Update(func(o *mcpclienttest.AuthOptions) { o.Intercept = nil })
	close(release)
	if err := <-done; !errors.Is(err, mcpclient.ErrUnknownSignIn) {
		t.Fatalf("Complete = %v", err)
	}
	if v := r.store.Values("shop"); len(v) != 0 {
		t.Fatalf("store = %v", v)
	}
	if _, _, err := r.oauth.Auth("shop").Header(context.Background()); !errors.Is(err, mcpclient.ErrNeedsSignIn) {
		t.Fatalf("Header = %v", err)
	}
	if revs := r.as.Requests("/revoke"); len(revs) != 2 || r.as.Valid(revs[1].Form.Get("token")) {
		t.Fatalf("revocations = %+v", revs)
	}
}
