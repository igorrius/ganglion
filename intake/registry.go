package intake

import (
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// IsAdminFunc reports whether principal is an admin (access.List.IsAdmin).
type IsAdminFunc func(ctx context.Context, principal string) (bool, error)

// EnqueueFunc writes outgoing messages to the outbox (sqlqueue.Queue.Enqueue).
type EnqueueFunc func(ctx context.Context, msgs ...event.Outgoing) error

// LangFunc is a person's language (language.Store.Resolve).
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// PrivateConvFunc turns a principal into its private conversation
// (telegram.PrivateConv); ok is false for one it cannot reach.
type PrivateConvFunc func(principal string) (transport, conv string, ok bool)

// OAuthAnswer is what an answered OAuth request hands over. The code is
// one-time and useless without the PKCE verifier its owner kept; it is
// still never logged.
type OAuthAnswer struct {
	ID     string // the state the request was opened with
	Code   string
	Issuer string // "" when the page sent none
}

// OAuthFunc receives the code of an answered OAuth request. The caller
// kept its PKCE verifier. An error closes the request as failed; the
// caller logs its own errors.
type OAuthFunc func(ctx context.Context, a OAuthAnswer) error

// SecretFunc receives the opened value of a secret request. The value is
// cleared once the function returns, so it must copy what it keeps. The
// write to the store is the caller's.
type SecretFunc func(ctx context.Context, id string, value []byte) error

// Outcome is how a request closed.
type Outcome string

// Outcomes of a request.
const (
	OutcomeAnswered Outcome = "answered"
	OutcomeFailed   Outcome = "failed" // the handler failed, or the ciphertext did not open
	OutcomeExpired  Outcome = "expired"
)

// Closed tells that a request closed (R-INT-6). It holds no value.
type Closed struct {
	ID      string
	Kind    Kind
	Outcome Outcome
	At      time.Time
}

// ClosedFunc is told when a request closes: answered, failed or expired.
// It is the hook for folding the request's card and for the agent's node
// note (R-INT-6). It is called with no lock held.
type ClosedFunc func(ctx context.Context, c Closed)

// NoClosed is the ClosedFunc that does nothing.
func NoClosed(context.Context, Closed) {}

// Config holds the registry's limits.
type Config struct {
	TTL     time.Duration // how long a request lives (R-INT-1)
	MaxOpen int           // how many requests may be open at once
	// HandleTimeout bounds an opener's function (an OAuth code exchange, a
	// store write), which runs off the transport's receive path.
	HandleTimeout time.Duration
}

// DefaultConfig is 10 minutes, 32 open requests and a minute per handler.
func DefaultConfig() Config {
	return Config{TTL: 10 * time.Minute, MaxOpen: 32, HandleTimeout: time.Minute}
}

// Errors of opening a request.
var (
	// ErrTooMany: MaxOpen requests are open.
	ErrTooMany = errors.New("intake: too many open requests")
	// ErrName: the secret's name is empty, too long, not UTF-8 or holds a
	// control character.
	ErrName = errors.New("intake: bad secret name")
)

// maxName is the longest secret name, in bytes.
const maxName = 128

// sweepEvery is how often Run closes expired requests.
const sweepEvery = 30 * time.Second

// request is one open request. A secret request holds the private key of
// its one-time pair.
type request struct {
	kind    Kind
	name    string
	expires time.Time
	oauth   OAuthFunc
	secret  SecretFunc
	key     hpke.PrivateKey
	closed  ClosedFunc // the opener's own hook; NoClosed when it set none
}

// destroy drops the private key. Go cannot zero an ecdh.PrivateKey; the
// reference is dropped so the key is collected, and the request can never
// open another line.
func (q *request) destroy() { q.key = nil }

// OAuthRequest is an open OAuth request.
type OAuthRequest struct {
	ID      string // the state the caller puts in the sign-in link
	Expires time.Time
	// PageErr is nil when the hosted page matched this release's copy;
	// otherwise ErrPageChanged or ErrPageUnchecked, and the card carries a
	// warning. A code is useless without the PKCE verifier the node keeps,
	// so the sign-in goes on (R-INT-3).
	PageErr error
}

// SecretRequest is an open secret request.
type SecretRequest struct {
	ID        string
	Name      string
	PublicKey []byte // the 65-byte uncompressed P-256 point the page seals to
	Expires   time.Time
}

// Registry holds the open requests, in memory only: a restart closes them
// (R-INT-1). Requests are opened by the node, never by the model: no tool
// exposes the registry, and nothing here reads a value back out.
type Registry struct {
	isAdmin IsAdminFunc
	enqueue EnqueueFunc
	lang    LangFunc
	private PrivateConvFunc
	texts   *texts.Catalog
	page    PageCheckFunc

	cfg    Config
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	random io.Reader
	log    *slog.Logger
	closed ClosedFunc
	tel    telemetry

	mu   sync.Mutex
	reqs map[string]*request
	work sync.WaitGroup // the openers' functions running, which Run waits for
}

// Option configures a Registry.
type Option func(*Registry)

// WithConfig sets the limits; the default is DefaultConfig.
func WithConfig(c Config) Option { return func(r *Registry) { r.cfg = c } }

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(r *Registry) { r.now = now } }

// WithAfter sets the wait between sweeps; the default is time.After.
func WithAfter(after func(time.Duration) <-chan time.Time) Option {
	return func(r *Registry) { r.after = after }
}

// WithRandom sets where ids come from; the default is crypto/rand.
func WithRandom(rd io.Reader) Option { return func(r *Registry) { r.random = rd } }

// WithLogger sets the logger; the default discards. The registry logs ids
// and kinds, never a line, a code or a value.
func WithLogger(l *slog.Logger) Option { return func(r *Registry) { r.log = l } }

// WithClosed sets the hook told when a request closes; the default is NoClosed.
func WithClosed(f ClosedFunc) Option { return func(r *Registry) { r.closed = f } }

// New returns a registry that answers admins in their private conversations
// through enqueue, with the texts of cat in each person's language. page
// checks the hosted intake page before every request (R-INT-3).
func New(isAdmin IsAdminFunc, enqueue EnqueueFunc, lang LangFunc, private PrivateConvFunc, cat *texts.Catalog,
	page PageCheckFunc, opts ...Option) *Registry {
	r := &Registry{isAdmin: isAdmin, enqueue: enqueue, lang: lang, private: private, texts: cat, page: page,
		cfg: DefaultConfig(), now: time.Now, after: time.After, random: rand.Reader,
		log: slog.New(slog.DiscardHandler), closed: NoClosed, reqs: map[string]*request{}}
	for _, o := range opts {
		o(r)
	}
	r.tel = newTelemetry(r.open)
	return r
}

// Name implements node.Component.
func (*Registry) Name() string { return "intake" }

// Run closes expired requests every 30 seconds until ctx ends, then waits
// for the openers' functions still running.
func (r *Registry) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			r.Wait()
			return nil
		case <-r.after(sweepEvery):
			r.sweep(ctx)
		}
	}
}

// Wait blocks until the openers' functions handed a value by Take have
// returned.
func (r *Registry) Wait() { r.work.Wait() }

// open is how many requests are open now.
func (r *Registry) open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// OpenOAuth opens a request for the code of an OAuth sign-in. Its ID is the
// state the caller puts in the sign-in link; handle gets the code when an
// admin pastes it. A hosted page that fails its check does not stop it:
// PageErr says so, for the card's warning.
func (r *Registry) OpenOAuth(ctx context.Context, handle OAuthFunc, opts ...OpenOption) (OAuthRequest, error) {
	pageErr := r.page(ctx)
	q := &request{kind: KindOAuth, oauth: handle, closed: NoClosed}
	for _, o := range opts {
		o(q)
	}
	id, err := r.add(ctx, q)
	if err != nil {
		return OAuthRequest{}, fmt.Errorf("intake: open oauth: %w", err)
	}
	if pageErr != nil {
		r.log.Warn("intake: the intake page failed its check; the sign-in goes on", "request", id, "error", pageErr)
	}
	return OAuthRequest{ID: id, Expires: q.expires, PageErr: pageErr}, nil
}

// OpenOption configures one request.
type OpenOption func(*request)

// OnClosed sets a hook told when this request closes, after the registry's
// own (WithClosed): the opener folds its card there (R-INT-6). It runs
// with no lock held, once.
func OnClosed(f ClosedFunc) OpenOption { return func(q *request) { q.closed = f } }

// OpenSecret opens a request for the secret called name. The page seals the
// value to the returned public key (R-INT-4); handle gets it opened. It
// first checks the hosted page: an error wrapping ErrPageChanged or
// ErrPageUnchecked refuses the request, and the caller tells the admin
// that handing secrets over is stopped (R-INT-3).
func (r *Registry) OpenSecret(ctx context.Context, name string, handle SecretFunc, opts ...OpenOption) (SecretRequest, error) {
	if err := checkName(name); err != nil {
		return SecretRequest{}, fmt.Errorf("intake: open secret: %w", err)
	}
	if err := r.page(ctx); err != nil {
		return SecretRequest{}, fmt.Errorf("intake: open secret: %w", err)
	}
	priv, err := ecdh.P256().GenerateKey(r.random)
	if err != nil {
		return SecretRequest{}, fmt.Errorf("intake: open secret: key: %w", err)
	}
	key, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		return SecretRequest{}, fmt.Errorf("intake: open secret: key: %w", err)
	}
	q := &request{kind: KindSecret, name: name, secret: handle, key: key, closed: NoClosed}
	for _, o := range opts {
		o(q)
	}
	id, err := r.add(ctx, q)
	if err != nil {
		return SecretRequest{}, fmt.Errorf("intake: open secret: %w", err)
	}
	return SecretRequest{ID: id, Name: name, PublicKey: priv.PublicKey().Bytes(), Expires: q.expires}, nil
}

// checkName refuses a name that is empty, over 128 bytes, not UTF-8, or
// holds a control character (the name is part of the cipher's info).
func checkName(name string) error {
	bad := len(name) == 0 || len(name) > maxName || !utf8.ValidString(name)
	if !bad {
		for _, c := range name {
			if unicode.IsControl(c) {
				bad = true
				break
			}
		}
	}
	if bad {
		return ErrName
	}
	return nil
}

// newID is 16 random bytes in base64url, 22 characters: what the page accepts.
func (r *Registry) newID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r.random, b[:]); err != nil {
		return "", fmt.Errorf("id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// add stores q under a new id, after closing the requests that expired.
func (r *Registry) add(ctx context.Context, q *request) (string, error) {
	r.sweep(ctx)
	id, err := r.newID()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) >= r.cfg.MaxOpen {
		return "", ErrTooMany
	}
	if _, taken := r.reqs[id]; taken {
		return "", errors.New("id taken")
	}
	q.expires = r.now().Add(r.cfg.TTL)
	r.reqs[id] = q
	return id, nil
}

// sweep removes the requests past their time and tells ClosedFunc, with
// no lock held.
func (r *Registry) sweep(ctx context.Context) {
	type gone struct {
		c Closed
		q *request
	}
	var all []gone
	r.mu.Lock()
	now := r.now()
	for id, q := range r.reqs {
		if !now.Before(q.expires) {
			q.destroy()
			delete(r.reqs, id)
			all = append(all, gone{c: Closed{ID: id, Kind: q.kind, Outcome: OutcomeExpired, At: now}, q: q})
		}
	}
	r.mu.Unlock()
	for _, g := range all {
		r.finish(ctx, g.q, g.c)
	}
}

// finish counts and reports a closed request: to the registry's hook, then
// to the request's own.
func (r *Registry) finish(ctx context.Context, q *request, c Closed) {
	r.tel.closed(ctx, c.Kind, c.Outcome)
	r.log.Info("intake: request closed", "request", c.ID, "kind", c.Kind, "outcome", c.Outcome)
	r.closed(ctx, c)
	q.closed(ctx, c)
}
