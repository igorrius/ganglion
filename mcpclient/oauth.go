package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/igorrius/ganglion/intake"
	"golang.org/x/oauth2"
)

// Errors of the sign-in; callers decide on them with errors.Is. None
// carries an address, a code, a token or a body.
var (
	// ErrNoSignIn is a server that does not ask for an OAuth sign-in: it
	// did not answer 401, or it names no metadata.
	ErrNoSignIn = errors.New("mcpclient: the server offers no OAuth sign-in")
	// ErrMetadata is sign-in metadata the node refuses: an address that is
	// not https, a document on another host than the server's, an issuer
	// or a resource that does not match, no PKCE S256, no registration.
	ErrMetadata = errors.New("mcpclient: unusable sign-in metadata")
	// ErrRegistration is a dynamic client registration that failed or was
	// answered with something the node cannot use.
	ErrRegistration = errors.New("mcpclient: client registration failed")
	// ErrUnknownSignIn is an answer to a sign-in that is not open: never
	// begun here, already answered, cancelled or expired.
	ErrUnknownSignIn = errors.New("mcpclient: no such sign-in open")
	// ErrIssuer is an answer whose `iss` is not the issuer's, or that lacks
	// it when the issuer sends it (RFC 9207): a mix-up attack, or a line
	// meant for another sign-in.
	ErrIssuer = errors.New("mcpclient: the answer comes from another issuer")
	// ErrExchange is a code the authorization server did not turn into a
	// usable token.
	ErrExchange = errors.New("mcpclient: the code was not exchanged")
	// ErrNeedsSignIn is a server whose token is gone or refused and cannot
	// be refreshed: the admin signs in again (R-MCP-6). It is also
	// ErrUnauthorized.
	ErrNeedsSignIn = fmt.Errorf("%w: needs a new sign-in", ErrUnauthorized)
)

// Store keeps the servers' sign-ins, by server: the registration and the
// tokens, under names of its own (R-MCP-5). The composition root gives one
// over the secret store's scope `mcp:<server>`. Get returns "" for a name
// that is not stored. Values are secrets: an implementation never logs
// them.
type Store interface {
	Get(ctx context.Context, server, name string) (string, error)
	Set(ctx context.Context, server, name, value string) error
	// Remove deletes everything stored for the server.
	Remove(ctx context.Context, server string) error
}

// OAuthConfig are the sign-in's limits.
type OAuthConfig struct {
	// Timeout bounds each step that goes to the network: discovery with
	// registration, an exchange, a refresh, a revocation.
	Timeout time.Duration
	// PendingTTL is how long a begun sign-in waits for its answer; the
	// intake request it rides on lives as long (R-INT-1).
	PendingTTL time.Duration
	// MaxPending is how many sign-ins may wait at once.
	MaxPending int
	// ClientName is the client_name the node registers with.
	ClientName string
}

// DefaultOAuthConfig is 30 s a step, 10 minutes for an answer, 32 waiting.
func DefaultOAuthConfig() OAuthConfig {
	return OAuthConfig{Timeout: DefaultTimeout, PendingTTL: 10 * time.Minute, MaxPending: 32, ClientName: "ganglion"}
}

// OAuth signs the node in to remote servers with OAuth 2.1 (R-MCP-3,
// R-MCP-5, R-MCP-6): discovery, dynamic registration, a sign-in link with
// PKCE S256, `state` and `resource`, the code exchange, the tokens in the
// Store, their refresh, and their revocation on Disconnect. The token
// flow is golang.org/x/oauth2's (ADR-0097). It is safe for concurrent use.
type OAuth struct {
	hc       *http.Client
	store    Store
	redirect string
	cfg      OAuthConfig
	now      func() time.Time
	log      *slog.Logger
	version  string
	in       oauthInstruments

	mu      sync.Mutex
	pending map[string]*pending
	auths   map[string]*tokenAuth
}

// OAuthOption configures an OAuth.
type OAuthOption func(*OAuth)

// WithOAuthConfig sets the limits; the default is DefaultOAuthConfig.
func WithOAuthConfig(c OAuthConfig) OAuthOption { return func(o *OAuth) { o.cfg = c } }

// WithOAuthClock sets the clock that ages sign-ins and tokens.
func WithOAuthClock(now func() time.Time) OAuthOption { return func(o *OAuth) { o.now = now } }

// WithOAuthLogger sets where a refused token and a failed revocation are
// logged; the default drops them. Lines carry the server and a kind, never
// a value.
func WithOAuthLogger(l *slog.Logger) OAuthOption { return func(o *OAuth) { o.log = l } }

// WithOAuthVersion sets the version the probe's clientInfo reports.
func WithOAuthVersion(v string) OAuthOption { return func(o *OAuth) { o.version = v } }

// NewOAuth makes the sign-in of the node's servers. Requests go through hc,
// which the composition root makes dial public addresses only; it follows
// no redirect. Registrations and tokens live in store; redirect is the
// intake page, the only redirect address the node registers (ADR-0098).
// Instruments come from the global meter provider.
func NewOAuth(hc *http.Client, store Store, redirect string, opts ...OAuthOption) *OAuth {
	o := &OAuth{store: store, redirect: redirect, cfg: DefaultOAuthConfig(), now: time.Now,
		log: slog.New(slog.DiscardHandler), version: devVersion, in: defaultOAuthInstruments(),
		pending: map[string]*pending{}, auths: map[string]*tokenAuth{}}
	for _, opt := range opts {
		opt(o)
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	o.hc = &c
	return o
}

// SignIn is a begun sign-in: the link the card carries and what the card
// names (R-MCP-3).
type SignIn struct {
	// URL is the authorization request: PKCE S256, `state`, `resource`.
	// It holds no secret; the verifier stays in the node's memory.
	URL string
	// ServerHost and SignInHost are the hosts of the server and of the
	// authorization endpoint, which may differ.
	ServerHost, SignInHost string
	Expires                time.Time
}

// pending is a sign-in waiting for its answer.
type pending struct {
	server   string
	reg      registration
	verifier string
	expires  time.Time
}

// maxState is the longest state taken: intake's ids are far shorter.
const maxState = 128

// Begin starts the sign-in of the server called server at serverURL, with
// state the id of the intake request that will carry the answer. It
// discovers the sign-in, registers the node when the store holds no
// registration for that issuer, and returns the link. Every address is
// checked like web.fetch's (R-MCP-3). A server that needs no sign-in is
// ErrNoSignIn; refused metadata is ErrMetadata.
func (o *OAuth) Begin(ctx context.Context, server, serverURL, state string) (SignIn, error) {
	if state == "" || len(state) > maxState || !validSessionID(state) {
		return SignIn{}, fmt.Errorf("mcpclient: begin the sign-in of %s: a bad state", server)
	}
	ctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()
	md, err := o.discover(ctx, serverURL)
	o.in.step(ctx, stepDiscover, err)
	if err != nil {
		return SignIn{}, fmt.Errorf("mcpclient: discover the sign-in of %s: %w", server, err)
	}
	reg, err := o.registration(ctx, server, md)
	if err != nil {
		return SignIn{}, fmt.Errorf("mcpclient: register with the sign-in of %s: %w", server, err)
	}
	verifier := oauth2.GenerateVerifier()
	p := &pending{server: server, reg: reg, verifier: verifier, expires: o.now().Add(o.cfg.PendingTTL)}
	if err := o.hold(state, p); err != nil {
		return SignIn{}, fmt.Errorf("mcpclient: begin the sign-in of %s: %w", server, err)
	}
	link := reg.config(o.redirect).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", reg.Resource))
	return SignIn{URL: link, ServerHost: hostOf(serverURL), SignInHost: hostOf(reg.AuthURL), Expires: p.expires}, nil
}

// hold keeps a begun sign-in under its state, dropping the expired ones.
func (o *OAuth) hold(state string, p *pending) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sweep()
	if _, ok := o.pending[state]; ok {
		return errors.New("a state already in use")
	}
	if len(o.pending) >= o.cfg.MaxPending {
		return errors.New("too many sign-ins waiting")
	}
	o.pending[state] = p
	return nil
}

// sweep drops expired sign-ins; the caller holds mu.
func (o *OAuth) sweep() {
	now := o.now()
	for k, p := range o.pending {
		if !now.Before(p.expires) {
			delete(o.pending, k)
		}
	}
}

// take removes the sign-in of state and returns it, if it is open.
func (o *OAuth) take(state string) (*pending, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sweep()
	p, ok := o.pending[state]
	delete(o.pending, state)
	return p, ok
}

// Cancel forgets the sign-in of state, if one waits; its answer is then
// ErrUnknownSignIn.
func (o *OAuth) Cancel(state string) { o.take(state) }

// Complete takes the answer of a sign-in, as intake hands it over (an
// intake.OAuthFunc): it checks `iss` (RFC 9207), exchanges the code with
// the PKCE verifier and `resource`, and stores the tokens. A sign-in is
// answered once: whatever the outcome, it is closed. An answer to no open
// sign-in is ErrUnknownSignIn; a wrong issuer ErrIssuer; a refused code
// ErrExchange.
func (o *OAuth) Complete(ctx context.Context, a intake.OAuthAnswer) error {
	p, ok := o.take(a.ID)
	if !ok {
		return ErrUnknownSignIn
	}
	if err := checkIssuer(p.reg, a.Issuer); err != nil {
		o.in.step(ctx, stepExchange, err)
		return fmt.Errorf("mcpclient: complete the sign-in of %s: %w", p.server, err)
	}
	ctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()
	tok, err := p.reg.config(o.redirect).Exchange(o.tokenContext(ctx, p.reg), a.Code, oauth2.VerifierOption(p.verifier))
	if err != nil {
		err = o.tokenFailure(ctx, err, ErrExchange)
	} else {
		err = o.auth(p.server).signedIn(ctx, p.reg, tok)
	}
	o.in.step(ctx, stepExchange, err)
	if err != nil {
		return fmt.Errorf("mcpclient: complete the sign-in of %s: %w", p.server, err)
	}
	return nil
}

// checkIssuer is RFC 9207: an issuer that sends `iss` must have sent this
// one; an `iss` that came anyway must be the issuer's.
func checkIssuer(reg registration, iss string) error {
	if iss == "" && !reg.IssParameter {
		return nil
	}
	if iss != reg.Issuer {
		return ErrIssuer
	}
	return nil
}

// Auth is the credential of the server called server for its Client: the
// stored access token, refreshed when it expires or after the server
// refused it. Without a usable token it is ErrNeedsSignIn.
func (o *OAuth) Auth(server string) Auth { return o.auth(server) }

// auth is the one tokenAuth of a server.
func (o *OAuth) auth(server string) *tokenAuth {
	o.mu.Lock()
	defer o.mu.Unlock()
	a, ok := o.auths[server]
	if !ok {
		a = &tokenAuth{o: o, server: server}
		o.auths[server] = a
	}
	return a
}

// Disconnect ends the server's sign-in: it revokes the refresh and the
// access token where the issuer names a revocation endpoint (RFC 7009),
// forgets any sign-in waiting for it, and removes everything stored for
// it. A failed revocation is logged and does not stop the removal; the
// error is the store's.
func (o *OAuth) Disconnect(ctx context.Context, server string) error {
	o.forget(server)
	a := o.auth(server)
	reg, tok, err := a.drop(ctx)
	if err == nil {
		o.revokeAll(ctx, server, reg, tok)
	}
	if err := o.store.Remove(ctx, server); err != nil {
		return fmt.Errorf("mcpclient: remove the sign-in of %s: %w", server, err)
	}
	return nil
}

// forget drops the sign-ins waiting for server.
func (o *OAuth) forget(server string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, p := range o.pending {
		if p.server == server {
			delete(o.pending, k)
		}
	}
}
