// Package modhttp is the loopback listener that serves the pages of the
// owner's modules (R-HTTP-1…3, ADR-0110, ADR-0118): each module with an
// http trigger answers under its own prefix, http://<addr>/<module>/, and
// the listener lists them at /. The module host runs a request as the
// module's; this package owns everything between the browser and that run:
// the bind to loopback, the Host and Origin checks, the path, the body and
// in-flight limits, and the headers of the answer, which the node sets and
// a module cannot weaken.
package modhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/pdk"
)

// Limits of a request (R-HTTP-2).
const (
	// MaxBody is the longest request body a module is given; a longer one
	// is refused with 413.
	MaxBody = 1 << 20
	// MaxAtOnce is how many requests of one module are admitted at once;
	// the module host refuses the next with ErrBusy.
	MaxAtOnce = 4
	// MaxAnswerBody is the longest body a module may answer with.
	MaxAnswerBody = 4 << 20
	// maxInFlight is how many requests the listener takes at once, of all
	// modules and the index; a further one is refused with 503.
	maxInFlight = 32
	// maxHeaderValue is the longest header value passed on either way.
	maxHeaderValue = 1024
)

// Refusals the module host returns for a request.
var (
	// ErrNoPage: no running module has the prefix, or it declares no such
	// route.
	ErrNoPage = errors.New("no such page")
	// ErrBusy: the module has MaxAtOnce requests already.
	ErrBusy = errors.New("the module is busy")
	// ErrFailed: the module failed or broke its contract.
	ErrFailed = errors.New("the module failed")
	// ErrTimedOut: the module ran past its time limit.
	ErrTimedOut = errors.New("the module ran past its time limit")
)

// MethodError is a route that exists, with other methods.
type MethodError struct{ Allow []string }

func (e *MethodError) Error() string { return "the route does not answer this method" }

// Page is a running module that serves pages.
type Page struct {
	Short  string   // the module's short name: its prefix
	Routes []string // "GET /", sorted
}

// Modules are the module host's side of the listener (modhost.Host).
type Modules interface {
	// Pages are the running modules with an http trigger, by short name.
	Pages() []Page
	// ServeHTTP runs one request of the module named short. req.Path is
	// the path inside its prefix; a refusal is one of the errors above.
	ServeHTTP(ctx context.Context, short string, req pdk.HTTPRequest) (pdk.HTTPAnswer, error)
}

// Headers the node sets on every answer (R-HTTP-3, ADR-0118). A module
// can set none of them: its headers pass only through answerHeaders.
var nodeHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"},
	{"X-Content-Type-Options", "nosniff"},
	{"Cross-Origin-Resource-Policy", sameOrigin},
	{"Cross-Origin-Opener-Policy", sameOrigin},
	{"Referrer-Policy", "no-referrer"},
	{"X-Frame-Options", "DENY"},
}

// sameOrigin is the value of the headers that keep a page to its own
// origin, and the Sec-Fetch-Site of a request from it.
const sameOrigin = "same-origin"

// requestHeaders are the headers a module is given, lower-case.
var requestHeaders = []string{"accept", "content-type", "if-none-match"}

// answerHeaders are the headers a module may set, lower-case; any other,
// Set-Cookie and the CORS headers among them, is dropped.
var answerHeaders = []string{"content-type", "cache-control", "etag"}

// Config is the listener's settings.
type Config struct {
	// Addr is the loopback host:port to bind (http.local_addr). The host is
	// 127.0.0.1, ::1 or localhost, which binds 127.0.0.1 without asking a
	// resolver.
	Addr                                                      string
	ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout time.Duration
	ShutdownTimeout                                           time.Duration
}

// DefaultConfig is a listener on addr with the node's timeouts: a request
// may wait for the three before it in its module, each within the module's
// time limit.
func DefaultConfig(addr string) Config {
	return Config{Addr: addr, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute, ShutdownTimeout: 5 * time.Second}
}

// Listener is the loopback listener of modules' pages. It is a node
// component.
type Listener struct {
	cfg  Config
	bind netip.AddrPort
	mods Modules
	log  *slog.Logger
	slot chan struct{} // the requests in flight, maxInFlight at most

	mu    sync.Mutex
	hosts map[string]bool // the Host values it answers: its own address
	addr  string          // the bound address, once Run listens
	bound chan struct{}   // closed once Run listens
}

// New is a listener for mods on cfg.Addr; an address that is not loopback
// is refused here, whatever the settings said.
func New(cfg Config, mods Modules, log *slog.Logger) (*Listener, error) {
	bind, err := loopback(cfg.Addr)
	if err != nil {
		return nil, err
	}
	return &Listener{cfg: cfg, bind: bind, mods: mods, log: log, slot: make(chan struct{}, maxInFlight), bound: make(chan struct{})}, nil
}

// loopback is the address to bind for addr: a loopback IP with a port, or
// localhost, which is 127.0.0.1. Anything else is an error.
func loopback(addr string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("modhttp: address %q: %w", addr, err)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("modhttp: address %q: the port is not a number", addr)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	// Only the addresses the Host check answers (setBound): another
	// loopback address would bind, and refuse every request.
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || (ip != netip.MustParseAddr("127.0.0.1") && ip != netip.IPv6Loopback()) {
		return netip.AddrPort{}, fmt.Errorf("modhttp: address %q is not 127.0.0.1, ::1 or localhost: modules' pages are served there only", addr)
	}
	return netip.AddrPortFrom(ip, uint16(p)), nil
}

// Name implements node.Component.
func (*Listener) Name() string { return "modhttp" }

// Addr is the address the listener is bound to, once it listens; "" before.
func (l *Listener) Addr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addr
}

// Bound is closed once the listener listens.
func (l *Listener) Bound() <-chan struct{} { return l.bound }

// Run implements node.Component: it listens on the loopback address until
// ctx ends. A port it cannot bind stops the node, so the owner learns of it
// from the start, not from a page that never opens.
func (l *Listener) Run(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", l.bind.String())
	if err != nil {
		return fmt.Errorf("modhttp: listen on %s (http.local_addr): %w", l.bind, err)
	}
	l.setBound(ln.Addr().String())
	srv := &http.Server{
		Handler: l, ReadHeaderTimeout: l.cfg.ReadHeaderTimeout, ReadTimeout: l.cfg.ReadTimeout,
		WriteTimeout: l.cfg.WriteTimeout, IdleTimeout: l.cfg.IdleTimeout, MaxHeaderBytes: 16 << 10,
		ErrorLog: slog.NewLogLogger(l.log.Handler(), slog.LevelDebug),
	}
	l.log.Info("modhttp: serving modules' pages", "addr", "http://"+l.Addr()+"/")
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("modhttp: serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	return nil
}

// setBound records the bound address and the Host values that reach it:
// 127.0.0.1 and localhost with its port, and [::1] when it is bound there;
// without the port too when the port is 80, as a browser sends it.
func (l *Listener) setBound(addr string) {
	ap := netip.MustParseAddrPort(addr)
	port := strconv.Itoa(int(ap.Port()))
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	if ap.Addr().Is6() {
		hosts["[::1]:"+port] = true
	}
	if port == "80" {
		for h := range hosts {
			hosts[strings.TrimSuffix(h, ":80")] = true
		}
	}
	l.mu.Lock()
	l.addr, l.hosts = addr, hosts
	l.mu.Unlock()
	close(l.bound)
}
