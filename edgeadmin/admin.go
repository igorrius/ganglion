// Package edgeadmin is the owner's side of connecting a computer as an edge
// (R-EDGE-2, R-EDGE-3, R-EDGE-5, ADR-0116): the agent's tool edge.join and
// the card it sends, the Approve and Cancel buttons that make the one-time
// code, the /edges card with a Revoke button for each joined edge, and the
// notice every admin gets when an edge joins. The codes, the keys and the
// list are edgejoin's; this package speaks chat and nothing else.
//
// A code is shown once, in the edited card. It never goes into a log, an
// error, a note of the history or any text the model reads.
package edgeadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// Service is the join (edgejoin.Service).
type Service interface {
	Propose(ctx context.Context, name string) (edgejoin.Proposal, error)
	Approve(ctx context.Context, name, digest string) (string, time.Time, error)
	Revoke(ctx context.Context, name string) error
	Edges(ctx context.Context) ([]edgejoin.Edge, error)
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

// AddressFunc is the node's public address as an edge reaches it, "" when
// the node does not know it (the node's http.public_url).
type AddressFunc func() string

// Deps are what the admin's side works with. Every field is required.
type Deps struct {
	Service Service
	Buttons ButtonStore
	Enqueue EnqueueFunc
	Lang    LangFunc
	Texts   *texts.Catalog
	IsAdmin IsAdminFunc
	Address AddressFunc
	// Private finds a person's private chat (telegram.PrivateConv).
	Private alert.PrivateConvFunc
	// Location is where times are shown (node.tz).
	Location *time.Location
}

// Admin is the owner's side of the edges. It is safe for concurrent use.
type Admin struct {
	d   Deps
	now func() time.Time
	log *slog.Logger
}

// Option configures an Admin.
type Option func(*Admin)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(a *Admin) { a.now = now } }

// WithLogger sets the logger; the default discards. Lines carry an edge's
// name and a kind, never a code or a key.
func WithLogger(l *slog.Logger) Option { return func(a *Admin) { a.log = l } }

// New makes the owner's side over d.
func New(d Deps, opts ...Option) *Admin {
	a := &Admin{d: d, now: time.Now, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Placeholders of the texts.
const (
	varName      = "name"
	varTime      = "time"
	varCommand   = "command"
	varPublish   = "publish"
	varSubscribe = "subscribe"
)

// buttonsTTL is how long a card's buttons can be pressed.
const buttonsTTL = 24 * time.Hour

// errNotAdmin refuses a request or a press by someone who is not an admin.
var errNotAdmin = errors.New("edgeadmin: only an admin connects or revokes edges")

// admin checks that principal is an admin.
func (a *Admin) admin(ctx context.Context, principal string) error {
	ok, err := a.d.IsAdmin(ctx, principal)
	if err != nil {
		return fmt.Errorf("edgeadmin: %w", err)
	}
	if !ok {
		return errNotAdmin
	}
	return nil
}

// timeIn formats t for a person in the node's location.
func (a *Admin) timeIn(t time.Time) string { return t.In(a.d.Location).Format("2 Jan 15:04") }

// QuickActions are the cards' buttons by action name, for the agent
// (agent.Deps.QuickActions).
func (a *Admin) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionApprove: a.approveByButton, ActionCancel: a.cancelByButton,
		ActionRevoke: a.revokeByButton}
}

// Tools are the admin's tools: edge.join.
func (a *Admin) Tools() []tool.Handler { return []tool.Handler{joinTool{a: a}} }

// ErrUnavailable is what Unavailable answers: the node takes no edges.
var ErrUnavailable = errors.New("edgeadmin: this node takes no edges")

// Unavailable is the Service of a node that cannot take edges: no trust
// chain (a locked store, an external bus without one) or no public
// listener to reach it through. It lists none and makes no code.
type Unavailable struct{}

// Propose implements Service.
func (Unavailable) Propose(context.Context, string) (edgejoin.Proposal, error) {
	return edgejoin.Proposal{}, ErrUnavailable
}

// Approve implements Service.
func (Unavailable) Approve(context.Context, string, string) (string, time.Time, error) {
	return "", time.Time{}, ErrUnavailable
}

// Revoke implements Service: there is no edge to revoke.
func (Unavailable) Revoke(context.Context, string) error { return edgejoin.ErrUnknownEdge }

// Edges implements Service.
func (Unavailable) Edges(context.Context) ([]edgejoin.Edge, error) { return nil, nil }
