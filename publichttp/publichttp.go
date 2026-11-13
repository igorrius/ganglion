// Package publichttp is the node's public listener: the one entry the
// outside world has to the node (R-CFG-11, ADR-0078). It serves plain HTTP
// behind a proxy that ends TLS, only the routes components registered and
// a health route, and answers 404 to anything else. It is never the
// metrics listener, which has no authentication and stays on loopback.
package publichttp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HealthPath answers a platform's health check and the node's own check
// that its public address reaches this process (Reaches).
const HealthPath = "/healthz"

// Name is the listener's name as a component.
const Name = "http"

// ErrUnreachable is returned by Reaches when the public address did not
// answer with this process's value.
var ErrUnreachable = errors.New("publichttp: the public address does not reach this process")

// Config is the listener's settings; DefaultConfig returns the defaults.
type Config struct {
	Addr string // host:port to listen on
	// MaxBody caps a request's body, in bytes; a longer one fails the
	// handler's read.
	MaxBody int64
	// MaxInFlight is how many requests are served at once; one more gets
	// 503.
	MaxInFlight int
	// The server's timeouts: a slow client never holds a connection.
	ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout time.Duration
	// ShutdownTimeout is how long requests in flight get to finish at a
	// stop.
	ShutdownTimeout time.Duration
	// CheckTries and CheckEvery are how often Reaches asks the public
	// address before it gives up: during a deploy the address may answer
	// from the process this one replaces for a few seconds.
	CheckTries int
	CheckEvery time.Duration
}

// DefaultConfig is a listener on addr with the limits of ADR-0078: the
// longest Telegram update measured was 18 KB, so 1 MiB is ample.
func DefaultConfig(addr string) Config {
	return Config{
		Addr: addr, MaxBody: 1 << 20, MaxInFlight: 16,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		ShutdownTimeout: 5 * time.Second, CheckTries: 5, CheckEvery: 3 * time.Second,
	}
}

// AfterFunc waits: time.After outside tests.
type AfterFunc func(time.Duration) <-chan time.Time

// Listener is the public listener. Routes are registered with Handle
// before Run.
type Listener struct {
	cfg   Config
	log   *slog.Logger
	after AfterFunc
	hc    *http.Client
	nonce string
	slots chan struct{}
	in    instruments

	mu     sync.Mutex
	routes map[string]http.Handler
	bound  chan struct{} // closed when the listener has its address
	addr   string
}

// New returns a listener that is not listening yet; Run does. It makes the
// value Reaches looks for, new at every start.
func New(cfg Config, log *slog.Logger, after AfterFunc) (*Listener, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("publichttp: start value: %w", err)
	}
	l := &Listener{
		cfg: cfg, log: log, after: after, nonce: hex.EncodeToString(raw),
		slots: make(chan struct{}, cfg.MaxInFlight), in: newInstruments(),
		routes: map[string]http.Handler{}, bound: make(chan struct{}),
		// The check asks one fixed path of the operator's own address and
		// follows nothing: a redirect is not this process answering.
		hc: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	l.routes[HealthPath] = http.HandlerFunc(l.health)
	return l, nil
}

// Handle serves the exact path with h. It is called while the node is
// built, before Run; a path registered twice keeps the last handler.
func (l *Listener) Handle(path string, h http.Handler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.routes[path] = h
}

// Name implements node.Component.
func (*Listener) Name() string { return Name }

// Run listens until ctx ends, then lets requests in flight finish for
// ShutdownTimeout and closes. An address that cannot be bound is an error:
// the file asked for a listener.
func (l *Listener) Run(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", l.cfg.Addr)
	if err != nil {
		return fmt.Errorf("publichttp: listen on %s: %w", l.cfg.Addr, err)
	}
	l.mu.Lock()
	l.addr = ln.Addr().String()
	l.mu.Unlock()
	close(l.bound)
	srv := &http.Server{
		Handler: l, ReadHeaderTimeout: l.cfg.ReadHeaderTimeout, ReadTimeout: l.cfg.ReadTimeout,
		WriteTimeout: l.cfg.WriteTimeout, IdleTimeout: l.cfg.IdleTimeout, MaxHeaderBytes: 16 << 10,
	}
	l.log.Info("publichttp: listening", "addr", l.addr)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("publichttp: serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("publichttp: serve: %w", err)
	}
	return nil
}

// Addr waits until the listener is bound or ctx ends and returns the
// address it listens on; "" when ctx ended first.
func (l *Listener) Addr(ctx context.Context) string {
	select {
	case <-l.bound:
	case <-ctx.Done():
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addr
}

// ServeHTTP serves a registered path under the limits and answers 404 to
// anything else: no listing, no redirect, no file.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	h, ok := l.routes[r.URL.Path]
	l.mu.Unlock()
	route := routeOther
	if ok {
		route = r.URL.Path
	}
	rec := &recorder{ResponseWriter: w, code: http.StatusOK}
	defer func() { l.in.count(r.Context(), route, rec.code) }()
	if !ok {
		http.NotFound(rec, r)
		return
	}
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	default:
		http.Error(rec, "busy", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(rec, r.Body, l.cfg.MaxBody)
	h.ServeHTTP(rec, r)
}

// health answers with this start's value.
func (l *Listener) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_, _ = io.WriteString(w, l.nonce)
}

// Reaches asks publicURL's health route for this start's value, up to
// CheckTries times. Any other answer, from an older process or from
// somebody else's server, is ErrUnreachable with the last reason; ctx's
// error when ctx ended.
func (l *Listener) Reaches(ctx context.Context, publicURL string) error {
	target := strings.TrimRight(publicURL, "/") + HealthPath
	var last error
	for try := range l.cfg.CheckTries {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-l.after(l.cfg.CheckEvery):
			}
		}
		if last = l.ask(ctx, target); last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.log.Warn("publichttp: the public address did not answer as this process", "try", try+1, "reason", last)
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, last)
}

// ask makes one check. Its errors name no address: the caller logs them.
func (l *Listener) ask(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("the address is not a URL")
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return errors.New("no answer")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(len(l.nonce))+1))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if subtle.ConstantTimeCompare(body, []byte(l.nonce)) != 1 {
		return errors.New("another process answered")
	}
	return nil
}

// recorder remembers the status a handler wrote, for the metric.
type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}
