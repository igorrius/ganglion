package mcpclienttest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// AuthOptions say how the fake authorization server behaves.
type AuthOptions struct {
	// IssParameter makes the metadata say that `iss` comes with the code
	// (RFC 9207); SignIn returns it either way.
	IssParameter bool
	// NoRevocation leaves the revocation endpoint out of the metadata.
	NoRevocation bool
	// ExpiresIn is an access token's lifetime in seconds; zero names none.
	ExpiresIn int
	// NoRefresh issues no refresh token.
	NoRefresh bool
	// RefuseRefresh answers every refresh with invalid_grant.
	RefuseRefresh bool
	// Metadata changes the metadata document before it is served.
	Metadata func(doc map[string]any)
	// Registered changes the registration's answer before it is sent.
	Registered func(doc map[string]any)
	// Intercept sees every request first; true means it answered.
	Intercept func(w http.ResponseWriter, r *http.Request) bool
}

// AuthRequest is a request the authorization server received.
type AuthRequest struct {
	Method string
	Path   string
	Form   url.Values // the form of a POST to the token or revocation endpoint
	Body   []byte     // the body of a registration
	Header http.Header
}

// grant is an issued code, waiting for its exchange.
type grant struct{ clientID, redirect, challenge, resource string }

// AuthServer is a fake OAuth 2.1 authorization server over TLS: metadata
// (RFC 8414), dynamic registration (RFC 7591), the authorization request
// checked without a browser (SignIn), codes with PKCE S256 and `resource`
// (RFC 8707), rotating refresh tokens and revocation (RFC 7009).
type AuthServer struct {
	*httptest.Server

	mu      sync.Mutex
	opts    AuthOptions
	clients map[string]string // client id → its redirect address
	codes   map[string]grant
	access  map[string]bool
	refresh map[string]string // refresh token → client id
	n       int
	reqs    []AuthRequest
}

// NewAuthServer starts the fake over TLS; it stops with the test.
func NewAuthServer(t testing.TB, o AuthOptions) *AuthServer {
	t.Helper()
	a := &AuthServer{opts: o, clients: map[string]string{}, codes: map[string]grant{},
		access: map[string]bool{}, refresh: map[string]string{}}
	a.Server = httptest.NewTLSServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

// Update changes the options.
func (a *AuthServer) Update(f func(*AuthOptions)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(&a.opts)
}

func (a *AuthServer) options() AuthOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts
}

// Issuer is the server's identifier.
func (a *AuthServer) Issuer() string { return a.URL }

// Requests are the requests received at path so far; "" is every one.
func (a *AuthServer) Requests(path string) []AuthRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AuthRequest
	for _, r := range a.reqs {
		if path == "" || r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// Valid reports whether token is a live access token: the fake MCP
// server's Bearer.
func (a *AuthServer) Valid(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.access[token]
}

// ExpireAccess makes every access token issued so far invalid, as a
// server that dropped them; refresh tokens still work.
func (a *AuthServer) ExpireAccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.access = map[string]bool{}
}

// RevokeAll makes every token issued so far invalid.
func (a *AuthServer) RevokeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.access, a.refresh = map[string]bool{}, map[string]string{}
}

// SignInResult is what the browser brings to the redirect address.
type SignInResult struct{ Code, State, Issuer string }

// SignIn is the admin signing in: it checks the authorization request the
// link makes (a registered client and redirect, response_type code, PKCE
// S256, state, resource) and issues a code, as the redirect would carry
// it. The code holds a ':' as a real one did.
func (a *AuthServer) SignIn(link string) (SignInResult, error) {
	u, err := url.Parse(link)
	if err != nil {
		return SignInResult{}, err
	}
	q := u.Query()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, AuthRequest{Method: http.MethodGet, Path: u.Path, Form: q})
	switch {
	case u.Scheme+"://"+u.Host != a.URL || u.Path != "/authorize":
		return SignInResult{}, errors.New("not this server's authorization endpoint")
	case q.Get("response_type") != "code":
		return SignInResult{}, errors.New("response_type is not code")
	case a.clients[q.Get("client_id")] == "" || a.clients[q.Get("client_id")] != q.Get("redirect_uri"):
		return SignInResult{}, errors.New("an unknown client or redirect address")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return SignInResult{}, errors.New("no PKCE S256")
	case q.Get("state") == "" || q.Get("resource") == "":
		return SignInResult{}, errors.New("no state or no resource")
	}
	a.n++
	code := fmt.Sprintf("code:%d:%s", a.n, q.Get("state"))
	a.codes[code] = grant{clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"),
		challenge: q.Get("code_challenge"), resource: q.Get("resource")}
	return SignInResult{Code: code, State: q.Get("state"), Issuer: a.URL}, nil
}

func (a *AuthServer) serve(w http.ResponseWriter, r *http.Request) {
	o := a.options()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	req := AuthRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	if r.Method == http.MethodPost && r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		req.Form, _ = url.ParseQuery(string(body))
	}
	a.mu.Lock()
	a.reqs = append(a.reqs, req)
	a.mu.Unlock()
	if o.Intercept != nil && o.Intercept(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-authorization-server":
		a.metadata(w, o)
	case r.Method == http.MethodPost && r.URL.Path == "/register":
		a.register(w, body, o)
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		a.token(w, req.Form, o)
	case r.Method == http.MethodPost && r.URL.Path == "/revoke":
		a.revoke(w, req.Form)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *AuthServer) metadata(w http.ResponseWriter, o AuthOptions) {
	doc := map[string]any{
		"issuer":                                         a.URL,
		"authorization_endpoint":                         a.URL + "/authorize",
		"token_endpoint":                                 a.URL + "/token",
		"registration_endpoint":                          a.URL + "/register",
		"response_types_supported":                       []string{"code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": o.IssParameter,
	}
	if !o.NoRevocation {
		doc["revocation_endpoint"] = a.URL + "/revoke"
	}
	if o.Metadata != nil {
		o.Metadata(doc)
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *AuthServer) register(w http.ResponseWriter, body []byte, o AuthOptions) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if json.Unmarshal(body, &in) != nil || len(in.RedirectURIs) != 1 || in.AuthMethod != "none" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata"})
		return
	}
	a.mu.Lock()
	a.n++
	id := fmt.Sprintf("client-%d", a.n)
	a.clients[id] = in.RedirectURIs[0]
	a.mu.Unlock()
	doc := map[string]any{"client_id": id, "redirect_uris": in.RedirectURIs, "token_endpoint_auth_method": "none"}
	if o.Registered != nil {
		o.Registered(doc)
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (a *AuthServer) token(w http.ResponseWriter, f url.Values, o AuthOptions) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var clientID string
	switch f.Get("grant_type") {
	case "authorization_code":
		g, ok := a.codes[f.Get("code")]
		delete(a.codes, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || g.clientID != f.Get("client_id") || g.redirect != f.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || g.resource != f.Get("resource") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		clientID = g.clientID
	case "refresh_token":
		id, ok := a.refresh[f.Get("refresh_token")]
		if !ok || o.RefuseRefresh || id != f.Get("client_id") || f.Get("resource") == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		delete(a.refresh, f.Get("refresh_token"))
		clientID = id
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	a.n++
	access := fmt.Sprintf("access-%d", a.n)
	a.access[access] = true
	doc := map[string]any{"access_token": access, "token_type": "bearer", "resource": f.Get("resource")}
	if o.ExpiresIn > 0 {
		doc["expires_in"] = o.ExpiresIn
	}
	if !o.NoRefresh {
		refresh := fmt.Sprintf("refresh-%d", a.n)
		a.refresh[refresh] = clientID
		doc["refresh_token"] = refresh
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *AuthServer) revoke(w http.ResponseWriter, f url.Values) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.access, f.Get("token"))
	delete(a.refresh, f.Get("token"))
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// MemStore is a Store in memory, by server and name.
type MemStore struct {
	mu sync.Mutex
	m  map[string]map[string]string
	// Fail, when set, is what every call returns.
	Fail error
}

// NewMemStore makes an empty store.
func NewMemStore() *MemStore { return &MemStore{m: map[string]map[string]string{}} }

// SetFail makes every later call return err; nil restores the store.
func (s *MemStore) SetFail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Fail = err
}

// Get implements mcpclient.Store.
func (s *MemStore) Get(_ context.Context, server, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return "", s.Fail
	}
	return s.m[server][name], nil
}

// Set implements mcpclient.Store.
func (s *MemStore) Set(_ context.Context, server, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	if s.m[server] == nil {
		s.m[server] = map[string]string{}
	}
	s.m[server][name] = value
	return nil
}

// Remove implements mcpclient.Store.
func (s *MemStore) Remove(_ context.Context, server string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	delete(s.m, server)
	return nil
}

// Values are what the store holds for server.
func (s *MemStore) Values(server string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	maps.Copy(out, s.m[server])
	return out
}
