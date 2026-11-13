// Package access is the list of principals allowed to talk to the bot,
// with their roles, and a sink filter that keeps strangers out of the
// inbox (docs/spec.md, 7.8).
package access

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Role is what an allowed principal may do.
type Role string

// Roles.
const (
	RoleAdmin  Role = "admin"
	RoleFamily Role = "family"
)

// ErrUnknownRole is returned by Allow for a role other than the known ones.
var ErrUnknownRole = errors.New("access: unknown role")

// List is the allowed table.
type List struct {
	db      *sql.DB
	now     func() time.Time
	dropped metric.Int64Counter
	admin   string // seeded at start
}

// Option configures a List.
type Option func(*List)

// WithAdmin seeds principal as an admin at start; "" seeds nobody.
func WithAdmin(principal string) Option { return func(l *List) { l.admin = principal } }

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(l *List) { l.now = now } }

// New migrates the "access" component and returns the list.
func New(ctx context.Context, db *sql.DB, opts ...Option) (*List, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "access", sub); err != nil {
		return nil, err
	}
	l := &List{db: db, now: time.Now}
	l.dropped, _ = otel.Meter("ganglion/access").Int64Counter("ganglion.access.dropped",
		metric.WithDescription("Messages kept out of the inbox, by reason."))
	for _, o := range opts {
		o(l)
	}
	if l.admin != "" {
		if err := l.Allow(ctx, l.admin, RoleAdmin, ""); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// Allow adds principal with role, or changes the role and name of a
// principal already listed.
func (l *List) Allow(ctx context.Context, principal string, role Role, name string) error {
	if role != RoleAdmin && role != RoleFamily {
		return fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	if principal == "" {
		return errors.New("access: empty principal")
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO allowed (principal, role, name, added_ms) VALUES (?, ?, ?, ?)
		 ON CONFLICT (principal) DO UPDATE SET role = excluded.role, name = excluded.name`,
		principal, string(role), name, l.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("access: allow %s: %w", principal, err)
	}
	return nil
}

// Remove takes principal off the list; removing an unlisted principal is
// not an error.
func (l *List) Remove(ctx context.Context, principal string) error {
	if _, err := l.db.ExecContext(ctx, `DELETE FROM allowed WHERE principal = ?`, principal); err != nil {
		return fmt.Errorf("access: remove %s: %w", principal, err)
	}
	return nil
}

// Role returns the role of principal and whether it is listed.
func (l *List) Role(ctx context.Context, principal string) (Role, bool, error) {
	var role string
	err := l.db.QueryRowContext(ctx, `SELECT role FROM allowed WHERE principal = ?`, principal).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("access: role of %s: %w", principal, err)
	}
	return Role(role), true, nil
}

// Name returns the name principal is listed under; "" when it is not
// listed or has none.
func (l *List) Name(ctx context.Context, principal string) (string, error) {
	var name string
	err := l.db.QueryRowContext(ctx, `SELECT name FROM allowed WHERE principal = ?`, principal).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("access: name of %s: %w", principal, err)
	}
	return name, nil
}

// Listed reports whether principal is on the list, in any role.
func (l *List) Listed(ctx context.Context, principal string) (bool, error) {
	_, ok, err := l.Role(ctx, principal)
	return ok, err
}

// IsAdmin reports whether principal is listed with the admin role. A
// principal that is not listed is not an admin.
func (l *List) IsAdmin(ctx context.Context, principal string) (bool, error) {
	role, ok, err := l.Role(ctx, principal)
	return ok && role == RoleAdmin, err
}

// Admins returns the admin principals, sorted.
func (l *List) Admins(ctx context.Context) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT principal FROM allowed WHERE role = ? ORDER BY principal`, string(RoleAdmin))
	if err != nil {
		return nil, fmt.Errorf("access: admins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("access: admins: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// fromPerson are the kinds of events a person sends; their payloads name
// the sender under "from" and the group chat under "group" (event.Message,
// event.Decision).
var fromPerson = map[event.Kind]bool{event.KindMessage: true, event.KindDecision: true}

// GroupsFunc reports whether a group chat is connected.
type GroupsFunc func(ctx context.Context, group string) (bool, error)

// UnknownGroupFunc is told about a group chat that is not connected, so
// an admin can be asked. An error means it could not ask.
type UnknownGroupFunc func(ctx context.Context, g event.GroupSeen) error

// Groups is what the filter knows about group chats.
type Groups struct {
	Has     GroupsFunc
	Unknown UnknownGroupFunc
}

// NoGroups connects no group and tells nobody.
func NoGroups() Groups {
	return Groups{
		Has:     func(context.Context, string) (bool, error) { return false, nil },
		Unknown: func(context.Context, event.GroupSeen) error { return nil },
	}
}

// reason is why an event is kept out of the inbox, for the metric.
type reason string

// Reasons.
const (
	reasonMalformed reason = "malformed"
	reasonStranger  reason = "stranger"
	reasonGroup     reason = "group"  // from a group chat that is not connected
	reasonJoined    reason = "joined" // the bot was added to a group: never for the agent
)

// Filter wraps next so that only these reach it (spec 7.8, R-DLV-17): in
// a private chat, messages and button presses of principals on the list;
// in a group chat, those of anyone when the group is connected, and of
// nobody, an admin included, when it is not. A group that is not connected
// is reported to g.Unknown when someone writes in it or adds the bot to it.
// A dropped event returns transport.ErrDropped, so the transport confirms
// it, answers a press at once, and nobody gets a reply; it is logged
// without its text. Events that no person sent pass through. An error
// reading the list or the groups, or reporting a group, is returned, so
// the transport offers the update again later.
func (l *List) Filter(next transport.Sink, g Groups, logger *slog.Logger) transport.Sink {
	return filter{l: l, next: next, g: g, log: logger}.sink
}

// filter is one Filter.
type filter struct {
	l    *List
	next transport.Sink
	g    Groups
	log  *slog.Logger
}

func (f filter) sink(ctx context.Context, e event.Event) error {
	if e.Kind == event.KindGroupJoined {
		return f.joined(ctx, e)
	}
	if !fromPerson[e.Kind] {
		return f.next(ctx, e)
	}
	var m event.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil || m.From == "" {
		return f.drop(ctx, e, reasonMalformed, event.Message{})
	}
	if m.Group != "" {
		return f.fromGroup(ctx, e, m)
	}
	_, ok, err := f.l.Role(ctx, m.From)
	if err != nil {
		return err
	}
	if !ok {
		return f.drop(ctx, e, reasonStranger, m)
	}
	return f.next(ctx, e)
}

// fromGroup passes an event of a connected group and reports a message of
// one that is not. A press reports nothing: only a message asks.
func (f filter) fromGroup(ctx context.Context, e event.Event, m event.Message) error {
	ok, err := f.g.Has(ctx, m.Group)
	if err != nil {
		return err
	}
	if ok {
		return f.next(ctx, e)
	}
	if e.Kind == event.KindMessage {
		seen := event.GroupSeen{Group: m.Group, Conv: e.Conv, Title: m.GroupTitle, From: m.From, Name: m.Name}
		if err := f.g.Unknown(ctx, seen); err != nil {
			return err
		}
	}
	return f.drop(ctx, e, reasonGroup, m)
}

// joined reports a group the bot was added to when it is not connected.
// The event is never for the agent.
func (f filter) joined(ctx context.Context, e event.Event) error {
	var g event.GroupSeen
	if err := json.Unmarshal(e.Payload, &g); err != nil || g.Group == "" || g.From == "" {
		return f.drop(ctx, e, reasonMalformed, event.Message{})
	}
	ok, err := f.g.Has(ctx, g.Group)
	if err != nil {
		return err
	}
	if !ok {
		if err := f.g.Unknown(ctx, g); err != nil {
			return err
		}
	}
	return f.drop(ctx, e, reasonJoined, event.Message{From: g.From, Name: g.Name})
}

// drop counts and logs an event kept out of the inbox, without its text.
func (f filter) drop(ctx context.Context, e event.Event, why reason, m event.Message) error {
	f.l.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(why))))
	level := slog.LevelWarn
	if why == reasonJoined {
		level = slog.LevelInfo // nothing was refused: the event is only never the agent's
	}
	f.log.Log(ctx, level, "access: event dropped", "reason", string(why), "kind", e.Kind, "id", e.ID,
		"principal", m.From, "name", m.Name, "conv", e.Conv, "source", e.Source, "text_len", len(m.Text))
	return fmt.Errorf("access: %s: %w", why, transport.ErrDropped)
}
