// Package wsproxy is the edges' entry to the bus (R-EDGE-4, ADR-0114): a
// handler for the public listener that tunnels a WebSocket upgrade on `/bus`
// to the embedded server's WebSocket listener on loopback. It checks no JWT
// itself: the server does, in operator mode, and refuses a connection with
// none, an expired, revoked, forged or unsigned one. What the proxy does is
// bound what reaches the server: only an upgrade, only two paths, only the
// loopback address, at most MaxTunnels at once, and no header the server
// could take for an identity.
package wsproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

const (
	// Path is where a client of the bus connects.
	Path = "/bus"
	// LeafPath is where an edge's leaf connects: a leaf dialing
	// `wss://<address>/bus` asks for this path.
	LeafPath = "/bus/leafnode"
)

// ErrNotLoopback is a target address that is not a loopback host:port: the
// proxy reaches nothing but this machine.
var ErrNotLoopback = errors.New("wsproxy: the target is not a loopback host:port")

// Config bounds the proxy; DefaultConfig returns the values of ADR-0114.
type Config struct {
	// MaxTunnels is how many tunnels are open at once; one more gets 503.
	MaxTunnels int
	// DialTimeout is how long the connection to the server may take.
	DialTimeout time.Duration
}

// DefaultConfig is the limits of ADR-0114.
func DefaultConfig() Config { return Config{MaxTunnels: 64, DialTimeout: 5 * time.Second} }

// Handler tunnels upgrades to one loopback address.
type Handler struct {
	target string
	log    *slog.Logger
	slots  chan struct{}
	proxy  *httputil.ReverseProxy
}

// New returns a handler for target, a loopback host:port (127.0.0.0/8, ::1
// or localhost). Anything else is ErrNotLoopback.
func New(target string, cfg Config, log *slog.Logger) (*Handler, error) {
	if err := checkLoopback(target); err != nil {
		return nil, err
	}
	if cfg.MaxTunnels <= 0 {
		return nil, errors.New("wsproxy: MaxTunnels must be positive")
	}
	h := &Handler{target: target, log: log, slots: make(chan struct{}, cfg.MaxTunnels)}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	h.proxy = &httputil.ReverseProxy{
		Rewrite: h.rewrite,
		// Only the target is ever dialed, and no proxy variable of the
		// environment is followed.
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", target)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Warn("wsproxy: the bus did not take the upgrade", "reason", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	return h, nil
}

func checkLoopback(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port == "" {
		return fmt.Errorf("%w", ErrNotLoopback)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w", ErrNotLoopback)
	}
	return nil
}

// rewrite points the outgoing request at the server and drops what the
// server has no use for: addresses and cookies the client set, and any
// credential in a header (a user's JWT travels inside the NATS protocol).
func (h *Handler) rewrite(r *httputil.ProxyRequest) {
	out := r.Out
	out.URL = &url.URL{Scheme: "http", Host: h.target, Path: "/"}
	if r.In.URL.Path == LeafPath {
		out.URL.Path = "/leafnode"
	}
	out.Host = h.target
	for _, k := range []string{"Cookie", "Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Origin"} {
		out.Header.Del(k)
	}
}

// ServeHTTP refuses what is not a WebSocket upgrade on one of the two
// paths, refuses beyond the cap, and otherwise tunnels until either side
// closes or the request's context ends.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path != Path && r.URL.Path != LeafPath:
		http.NotFound(w, r)
		return
	case r.Method != http.MethodGet:
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	case !isUpgrade(r):
		http.Error(w, "a websocket upgrade is required", http.StatusBadRequest)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	h.proxy.ServeHTTP(w, r)
}

// isUpgrade says whether r asks for a WebSocket upgrade.
func isUpgrade(r *http.Request) bool {
	return headerHas(r.Header, "Connection", "upgrade") && headerHas(r.Header, "Upgrade", "websocket")
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}
