// Package mcpadmin is the admin's side of the remote MCP servers (R-MCP-2,
// R-MCP-3, R-MCP-9, R-MCP-10, ADR-0097): the mcp: section of the node's
// file applied to the hub, the agent's tools mcp.connect and mcp.rule, the
// cards the node writes for a sign-in and for a server's tools, the /mcp
// card with its Disconnect buttons, and a /status line for each server that
// is not connected. The client itself is mcpclient's; the requests a
// sign-in rides on are intake's.
package mcpadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// File is the mcp: section of the node's file, every change recorded
// (confighist.MCPFile, R-CFG-13).
type File interface {
	MCP() (settings.MCP, error)
	SetServer(name string, s settings.MCPServer, why string) error
	SetPolicy(server, tool string, p settings.MCPPolicy) error
	RemoveServer(name string) error
}

// Hub runs the servers (mcpclient.Hub).
type Hub interface {
	Set(servers []mcpclient.Server)
	States() []mcpclient.Status
}

// SignIns is the OAuth sign-in of the servers (mcpclient.OAuth).
type SignIns interface {
	Begin(ctx context.Context, server, serverURL, state string) (mcpclient.SignIn, error)
	Complete(ctx context.Context, a intake.OAuthAnswer) error
	Cancel(state string)
	Auth(server string) mcpclient.Auth
	Disconnect(ctx context.Context, server string) error
	SignedIn(ctx context.Context, server string) (time.Time, error)
}

// Requests opens the intake request a sign-in's answer comes back on
// (intake.Registry).
type Requests interface {
	OpenOAuth(ctx context.Context, handle intake.OAuthFunc) (intake.OAuthRequest, error)
}

// ButtonStore keeps the cards' buttons (pending.Store).
type ButtonStore interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// EnqueueFunc writes outgoing messages to the outbox (sqlqueue.Queue.Enqueue).
type EnqueueFunc func(ctx context.Context, msgs ...event.Outgoing) error

// LangFunc is a person's language (language.Store.Resolve).
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// IsAdminFunc reports whether principal is an admin (access.List.IsAdmin).
type IsAdminFunc func(ctx context.Context, principal string) (bool, error)

// ReservedFunc lists the prefixes a server's name may not take: the core's
// tool areas and the modules' short names (R-MCP-2).
type ReservedFunc func() []string

// LockedFunc reports whether the secret store is locked (R-CFG-10), when a
// server that signs in cannot reach its credential.
type LockedFunc func() bool

// Deps are what the admin's side works with. Every field is required; a
// node without a part gets that part's no-op.
type Deps struct {
	File     File
	Hub      Hub
	SignIns  SignIns
	Keys     Keys
	Requests Requests
	Buttons  ButtonStore
	Enqueue  EnqueueFunc
	Lang     LangFunc
	Texts    *texts.Catalog
	IsAdmin  IsAdminFunc
	Reserved ReservedFunc
	Locked   LockedFunc
	// HTTP is the client a probe goes through; the composition root gives
	// one that dials public addresses only (R-WEB-2).
	HTTP *http.Client
}

// Admin is the admin's side of the servers. It is safe for concurrent use.
type Admin struct {
	d       Deps
	now     func() time.Time
	log     *slog.Logger
	version string
	in      instruments
}

// Option configures an Admin.
type Option func(*Admin)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(a *Admin) { a.now = now } }

// WithLogger sets the logger; the default discards. Lines carry a server's
// name and a kind, never an address, a code or a value.
func WithLogger(l *slog.Logger) Option { return func(a *Admin) { a.log = l } }

// WithVersion sets the version a probe's clientInfo reports.
func WithVersion(v string) Option { return func(a *Admin) { a.version = v } }

// New makes the admin's side over d. Instruments come from the global meter
// provider.
func New(d Deps, opts ...Option) *Admin {
	a := &Admin{d: d, now: time.Now, log: slog.New(slog.DiscardHandler), version: "dev", in: newInstruments()}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Apply reads the mcp: section and hands its servers to the hub, which
// adds, removes and restarts them at once (R-CFG-6). The node calls it at
// start and after every change its tools make.
func (a *Admin) Apply() error {
	m, err := a.d.File.MCP()
	if err != nil {
		return fmt.Errorf("mcpadmin: read the mcp section: %w", err)
	}
	servers := make([]mcpclient.Server, 0, len(m.Servers))
	for _, name := range slices.Sorted(maps.Keys(m.Servers)) {
		servers = append(servers, a.server(name, m.Servers[name]))
	}
	a.d.Hub.Set(servers)
	return nil
}

// server is a line of the file as the hub takes it.
func (a *Admin) server(name string, s settings.MCPServer) mcpclient.Server {
	policies := make(map[string]tool.Policy, len(s.Tools))
	for t, p := range s.Tools {
		policies[t] = tool.Policy(p)
	}
	return mcpclient.Server{Name: name, URL: s.URL, Auth: a.auth(name, s), Timeout: time.Duration(s.Timeout),
		Policies: policies, Fingerprint: s.Fingerprint}
}

// auth is the credential a server's line names.
func (a *Admin) auth(name string, s settings.MCPServer) mcpclient.Auth {
	switch s.Auth {
	case settings.MCPAuthOAuth:
		return a.d.SignIns.Auth(name)
	case settings.MCPAuthHeader:
		return a.d.Keys.Auth(name, s.Header)
	case settings.MCPAuthNone:
	}
	return mcpclient.NoAuth{}
}

// line is the server's line in the file; ok is false when it has none.
func (a *Admin) line(name string) (settings.MCPServer, bool, error) {
	m, err := a.d.File.MCP()
	if err != nil {
		return settings.MCPServer{}, false, fmt.Errorf("mcpadmin: read the mcp section: %w", err)
	}
	s, ok := m.Servers[name]
	return s, ok, nil
}

// speaker writes in principal's language; English when it is unknown.
func (a *Admin) speaker(ctx context.Context, principal string) texts.Speaker {
	l, err := a.d.Lang(ctx, principal)
	if err != nil {
		l = texts.English
	}
	return texts.Speaker{Lang: l, Cat: a.d.Texts}
}

// errNotAdmin refuses a press by someone who is not an admin.
var errNotAdmin = errors.New("mcpadmin: only an admin changes the MCP servers")

// admin checks that principal is an admin.
func (a *Admin) admin(ctx context.Context, principal string) error {
	ok, err := a.d.IsAdmin(ctx, principal)
	if err != nil {
		return fmt.Errorf("mcpadmin: %w", err)
	}
	if !ok {
		return errNotAdmin
	}
	return nil
}
