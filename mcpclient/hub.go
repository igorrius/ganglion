package mcpclient

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/tool"
)

// State is where a server stands (R-MCP-7, R-MCP-8, R-MCP-10). The values
// are shown to the admin and never change.
type State string

// States.
const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateUnreachable  State = "unreachable"
	StateToolsChanged State = "tools_changed" // the list no longer matches the fingerprint
	StateUnauthorized State = "unauthorized"  // needs a new sign-in
)

// Server is a connected server as the admin approved it; the mcp: section
// of the settings gives it (R-MCP-2).
type Server struct {
	Name string // [a-z0-9_]{1,24}, checked where the section is read
	URL  string
	Auth Auth // nil sends no header
	// Timeout is how long one request may take; zero or less is
	// DefaultTimeout, since the section's default is applied by the caller
	// that reads the file.
	Timeout time.Duration
	// Policies are the policy of each approved tool, by its short name
	// (ToolName). A listed tool that has none is not approved.
	Policies map[string]tool.Policy
	// Fingerprint is the pin of the approved list (R-MCP-7).
	Fingerprint string
}

// Status is one server's standing, for /mcp and /status.
type Status struct {
	Name    string
	State   State
	Tools   int    // the approved tools
	Skipped []Skip // listed and not taken (R-MCP-4)
}

// AfterFunc waits d: the channel gets the time when it has passed. The
// default is time.After; a test injects its own, so none sleeps.
type AfterFunc func(d time.Duration) <-chan time.Time

// StateFunc is told that a server entered a state. It is called from the
// goroutine that noticed, with no lock held, so it must not block.
type StateFunc func(server string, st State)

// Hub keeps the configured servers: it connects to each, pins its tool
// list, and offers the approved tools (Handlers) and the tools that find
// and turn them on (Tools). It is a component (Run).
type Hub struct {
	log     *slog.Logger
	now     func() time.Time
	after   AfterFunc
	hc      *http.Client
	backoff backoff.Policy
	version string
	observe StateFunc
	in      instruments

	mu      sync.RWMutex
	servers map[string]*serverConn
	ctx     context.Context
	running bool
	wg      sync.WaitGroup
}

// HubOption configures a Hub.
type HubOption func(*Hub)

// WithHubLogger sets where connect failures are logged; the default drops
// them.
func WithHubLogger(l *slog.Logger) HubOption { return func(h *Hub) { h.log = l } }

// WithHubClock sets the clock that times calls.
func WithHubClock(now func() time.Time) HubOption { return func(h *Hub) { h.now = now } }

// WithHubAfter sets how the hub waits between retries.
func WithHubAfter(f AfterFunc) HubOption { return func(h *Hub) { h.after = f } }

// WithHubHTTPClient sets the HTTP client every server's requests use: the
// composition root gives one that dials public addresses only.
func WithHubHTTPClient(hc *http.Client) HubOption { return func(h *Hub) { h.hc = hc } }

// WithHubBackoff sets the wait between reconnects.
func WithHubBackoff(p backoff.Policy) HubOption { return func(h *Hub) { h.backoff = p } }

// WithHubObserver sets who is told when a server's state changes: the card
// of a server that needs a sign-in, `/status` (R-MCP-6, R-MCP-10).
func WithHubObserver(f StateFunc) HubOption { return func(h *Hub) { h.observe = f } }

// WithHubVersion sets the version clientInfo reports.
func WithHubVersion(v string) HubOption { return func(h *Hub) { h.version = v } }

// DefaultBackoff is the wait between reconnects: one second doubling to
// five minutes.
func DefaultBackoff() backoff.Policy { return backoff.Policy{Min: time.Second, Max: 5 * time.Minute} }

// NewHub makes a hub with no servers; Set gives it some and Run connects.
// Instruments come from the global meter provider, so make it after the
// telemetry is installed.
func NewHub(opts ...HubOption) *Hub {
	h := &Hub{log: slog.New(slog.DiscardHandler), now: time.Now, after: time.After, hc: &http.Client{},
		backoff: DefaultBackoff(), version: devVersion, observe: func(string, State) {}, in: defaultInstruments(), servers: map[string]*serverConn{}}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Set replaces the configured servers: a new one is added, a missing one is
// removed and a changed one restarts; an unchanged one is left alone. It is
// safe to call before Run and while it runs. A server with an empty name,
// or a name an earlier one has, is dropped.
func (h *Hub) Set(servers []Server) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[string]*serverConn, len(servers))
	for _, cfg := range servers {
		cfg = h.normal(cfg)
		if cfg.Name == "" || next[cfg.Name] != nil {
			continue
		}
		if old := h.servers[cfg.Name]; old != nil && sameServer(old.cfg, cfg) {
			next[cfg.Name] = old
			continue
		}
		next[cfg.Name] = h.newConn(cfg)
	}
	for name, old := range h.servers {
		if next[name] != old {
			old.stop()
		}
	}
	h.servers = next
	if h.running {
		for _, s := range next {
			h.start(s)
		}
	}
}

// normal fills in what a Server may leave out.
func (h *Hub) normal(cfg Server) Server {
	if cfg.Auth == nil {
		cfg.Auth = NoAuth{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return cfg
}

// sameServer reports whether two settings of a server are the same.
func sameServer(a, b Server) bool {
	return a.Name == b.Name && a.URL == b.URL && a.Timeout == b.Timeout && a.Fingerprint == b.Fingerprint &&
		maps.Equal(a.Policies, b.Policies) && reflect.DeepEqual(a.Auth, b.Auth)
}

// start runs the server's goroutine if it is not running. The caller holds
// h.mu and the hub is running.
func (h *Hub) start(s *serverConn) {
	if s.started {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(h.ctx)
	s.cancel = cancel
	h.wg.Go(func() { s.run(ctx) })
}

// Run connects to the servers and keeps them until ctx ends, then waits for
// every goroutine it started. It returns nil.
func (h *Hub) Run(ctx context.Context) error {
	h.mu.Lock()
	h.ctx, h.running = ctx, true
	for _, s := range h.servers {
		h.start(s)
	}
	h.mu.Unlock()
	<-ctx.Done()
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.wg.Wait()
	return nil
}

// names are the configured servers' names, sorted. The caller holds h.mu.
func (h *Hub) names() []string { return slices.Sorted(maps.Keys(h.servers)) }

// conn is the server called name.
func (h *Hub) conn(name string) (*serverConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.servers[name]
	return s, ok
}

// States lists each configured server's standing, by name.
func (h *Hub) States() []Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Status, 0, len(h.servers))
	for _, name := range h.names() {
		out = append(out, h.servers[name].status())
	}
	return out
}

// configured reports whether any server is set.
func (h *Hub) configured() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.servers) > 0
}

// Handlers are the approved tools of every connected server, the policy
// `forbidden` ones left out: the registry's dynamic source
// (tool.WithDynamic). Each is on demand (R-MCP-4a). It never blocks.
func (h *Hub) Handlers() []tool.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []tool.Handler
	for _, name := range h.names() {
		out = append(out, h.servers[name].offered()...)
	}
	return out
}
