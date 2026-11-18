package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Names in the Store (R-MCP-5).
const (
	nameClient = "oauth_client" // the registration and the endpoints
	nameToken  = "oauth_token"  // the access token, its expiry and the refresh token
)

// Limits and margins of the tokens.
const (
	// expirySkew is how long before its expiry a token is refreshed; a
	// token that lives less than twice as long is refreshed at half its
	// life.
	expirySkew = time.Minute
	// maxForm is the biggest token request body the resource is added to.
	maxForm = 64 << 10
)

// storedToken is what the store keeps under nameToken.
type storedToken struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token,omitempty"`
	Expiry  time.Time `json:"expiry,omitzero"` // when to refresh: before the real expiry; zero: none named
}

// tokenAuth is one server's OAuth credential: the Auth its Client gets. It
// loads the token from the store on first use and refreshes it when it
// expires or after the server refused it once (R-MCP-6). One mutex holds
// a refresh, so concurrent requests share it.
type tokenAuth struct {
	o      *OAuth
	server string

	mu        sync.Mutex
	loaded    bool
	reg       registration
	tok       storedToken
	force     bool // the server refused the token: refresh before the next request
	forced    bool // the token in hand came from such a refresh
	signedOut bool // no usable token until a new sign-in
}

// Header implements Auth: a bearer token, refreshed first when needed.
func (a *tokenAuth) Header(ctx context.Context) (name, value string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(ctx); err != nil {
		return "", "", err
	}
	if a.signedOut || a.tok.Access == "" && a.tok.Refresh == "" {
		return "", "", ErrNeedsSignIn
	}
	if a.force || a.tok.Access == "" || a.expiring() {
		if err := a.renew(ctx); err != nil {
			return "", "", err
		}
	}
	return "Authorization", "Bearer " + a.tok.Access, nil
}

// Rejected implements Auth. A token the server refused is refreshed once;
// one that came from such a refresh, or that cannot be refreshed, means a
// new sign-in.
func (a *tokenAuth) Rejected(context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.forced || a.tok.Refresh == "" {
		a.signedOut = true
		a.o.log.Warn("mcpclient: the server refused its token; it needs a new sign-in", "server", a.server)
		return
	}
	a.force = true
	a.o.log.Info("mcpclient: the server refused its token; refreshing it", "server", a.server)
}

// expiring reports whether the token is due for a refresh.
func (a *tokenAuth) expiring() bool {
	return !a.tok.Expiry.IsZero() && !a.o.now().Before(a.tok.Expiry)
}

// load reads the registration and the token once. A value that does not
// parse is no sign-in; a store that fails is tried again next time.
func (a *tokenAuth) load(ctx context.Context) error {
	if a.loaded {
		return nil
	}
	var reg registration
	var tok storedToken
	okReg, err := a.o.loadJSON(ctx, a.server, nameClient, &reg)
	if err != nil {
		return err
	}
	okTok, err := a.o.loadJSON(ctx, a.server, nameToken, &tok)
	if err != nil {
		return err
	}
	a.loaded, a.reg, a.tok = true, reg, tok
	a.signedOut = !okReg || !okTok || reg.ClientID == ""
	return nil
}

// renew refreshes the token. A refusal means a new sign-in; a server that
// cannot be reached leaves things as they were, to try again.
func (a *tokenAuth) renew(ctx context.Context) error {
	if a.tok.Refresh == "" {
		a.signedOut = true
		return ErrNeedsSignIn
	}
	ctx, cancel := context.WithTimeout(ctx, a.o.cfg.Timeout)
	defer cancel()
	src := a.reg.config(a.o.redirect).TokenSource(a.o.tokenContext(ctx, a.reg), &oauth2.Token{RefreshToken: a.tok.Refresh})
	tok, err := src.Token()
	if err != nil {
		err = a.o.tokenFailure(ctx, err, ErrNeedsSignIn)
	} else {
		err = a.keep(ctx, tok)
	}
	a.o.in.step(ctx, stepRefresh, err)
	switch {
	case errors.Is(err, ErrNeedsSignIn):
		a.signedOut = true
		a.o.log.Warn("mcpclient: the token was not refreshed; the server needs a new sign-in", "server", a.server)
		return err
	case err != nil:
		return err
	}
	a.forced, a.force = a.force, false
	return nil
}

// signedIn takes the tokens of a completed sign-in.
func (a *tokenAuth) signedIn(ctx context.Context, reg registration, tok *oauth2.Token) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reg, a.tok = reg, storedToken{}
	if err := a.keep(ctx, tok); err != nil {
		a.loaded, a.signedOut = true, true
		return err
	}
	a.loaded, a.signedOut, a.force, a.forced = true, false, false, false
	return nil
}

// keep checks a token the server issued and stores it. A refresh that
// gives no new refresh token keeps the old one (x/oauth2 does).
func (a *tokenAuth) keep(ctx context.Context, tok *oauth2.Token) error {
	if tok.AccessToken == "" || len(tok.AccessToken) > 8<<10 {
		return fmt.Errorf("%w: no usable access token", ErrExchange)
	}
	if t := tok.TokenType; t != "" && !strings.EqualFold(t, "bearer") {
		return fmt.Errorf("%w: a token that is not a bearer token", ErrExchange)
	}
	st := storedToken{Access: tok.AccessToken, Refresh: tok.RefreshToken}
	if tok.ExpiresIn > 0 {
		life := time.Duration(tok.ExpiresIn) * time.Second
		st.Expiry = a.o.now().Add(life - min(expirySkew, life/2))
	}
	if err := a.o.saveJSON(ctx, a.server, nameToken, st); err != nil {
		return err
	}
	a.tok = st
	return nil
}

// drop forgets the credential for Disconnect and returns what was stored,
// for the revocation.
func (a *tokenAuth) drop(ctx context.Context) (registration, storedToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.load(ctx)
	reg, tok := a.reg, a.tok
	a.loaded, a.signedOut, a.force, a.forced = true, true, false, false
	a.reg, a.tok = registration{}, storedToken{}
	return reg, tok, err
}

// tokenFailure turns a failed token request into the client's own error:
// refused when the server answered (an RFC 6749 error, a body that does
// not parse, a redirect), unreachable when it did not or failed itself.
// The library's text, which carries the body, is dropped.
func (o *OAuth) tokenFailure(ctx context.Context, err error, refused error) error {
	var re *oauth2.RetrieveError
	var ue *url.Error
	switch {
	case errors.As(err, &re) && re.Response != nil && re.Response.StatusCode >= 500:
		return &transportError{text: fmt.Sprintf("the sign-in server answered status %d", re.Response.StatusCode)}
	case errors.As(err, &re), errors.Is(err, ErrRedirect):
		return refused
	case errors.As(err, &ue):
		return o.requestFailure(ctx, err)
	}
	return refused
}

// tokenContext is ctx carrying the HTTP client x/oauth2 uses for a token
// request: the node's, following no redirect, adding `resource` (RFC 8707)
// to every request, the refresh included, which x/oauth2 has no parameter
// for.
func (o *OAuth) tokenContext(ctx context.Context, reg registration) context.Context {
	hc := *o.hc
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = resourceParam{base: base, resource: reg.Resource}
	return context.WithValue(ctx, oauth2.HTTPClient, &hc)
}

// resourceParam adds `resource` to a form posted to the token endpoint.
type resourceParam struct {
	base     http.RoundTripper
	resource string
}

// RoundTrip implements http.RoundTripper on a copy of the request.
func (t resourceParam) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.Body == nil || r.Body == http.NoBody {
		return t.base.RoundTrip(r)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxForm+1))
	_ = r.Body.Close()
	if err != nil || len(data) > maxForm {
		return nil, errors.New("mcpclient: a token request that cannot be read")
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, errors.New("mcpclient: a token request that does not parse")
	}
	if form.Get("resource") == "" {
		form.Set("resource", t.resource)
	}
	body := form.Encode()
	c := r.Clone(r.Context())
	c.Body = io.NopCloser(strings.NewReader(body))
	c.ContentLength = int64(len(body))
	c.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }
	return t.base.RoundTrip(c)
}

// revokeAll revokes the refresh token, then the access token (RFC 7009),
// where the issuer names an endpoint. Failures are logged by kind.
func (o *OAuth) revokeAll(ctx context.Context, server string, reg registration, tok storedToken) {
	if reg.RevokeURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()
	for _, t := range []struct{ value, hint string }{{tok.Refresh, "refresh_token"}, {tok.Access, "access_token"}} {
		if t.value == "" {
			continue
		}
		err := o.revoke(ctx, reg, t.value, t.hint)
		o.in.step(ctx, stepRevoke, err)
		if err != nil {
			o.log.Warn("mcpclient: a token was not revoked", "server", server, "hint", t.hint, "kind", kindOf(err))
		}
	}
}

// revoke sends one revocation request; 200 is success (RFC 7009, 2.2).
func (o *OAuth) revoke(ctx context.Context, reg registration, token, hint string) error {
	form := url.Values{"token": {token}, "token_type_hint": {hint}}
	if reg.AuthMethod != authMethodBasic {
		form.Set("client_id", reg.ClientID)
		if reg.ClientSecret != "" {
			form.Set("client_secret", reg.ClientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reg.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return metadataError("a request that cannot be built")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if reg.AuthMethod == authMethodBasic {
		req.SetBasicAuth(url.QueryEscape(reg.ClientID), url.QueryEscape(reg.ClientSecret))
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		return o.requestFailure(ctx, err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}

// loadJSON reads a stored value into into; found is false for none, or for
// one that does not parse.
func (o *OAuth) loadJSON(ctx context.Context, server, name string, into any) (found bool, err error) {
	v, err := o.store.Get(ctx, server, name)
	if err != nil {
		return false, fmt.Errorf("mcpclient: read the sign-in of %s: %w", server, err)
	}
	if v == "" {
		return false, nil
	}
	return json.Unmarshal([]byte(v), into) == nil, nil
}

// saveJSON stores v under name.
func (o *OAuth) saveJSON(ctx context.Context, server, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcpclient: encode the sign-in of %s: %w", server, err)
	}
	if err := o.store.Set(ctx, server, name, string(data)); err != nil {
		return fmt.Errorf("mcpclient: store the sign-in of %s: %w", server, err)
	}
	return nil
}
