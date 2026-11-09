package tool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// PlacementLocal is where a tool runs on the node that handles the turn.
// It is the only place P1 has (R-RTR-3).
const PlacementLocal = "local"

// Placement says where a tool runs. The registry asks it before every
// run and refuses a tool that is not local. Declared here, by its
// consumer; router.Local is the P1 implementation (R-RTR-3).
type Placement interface {
	Where(tool string) string
}

// ErrNotLocal is a call to a tool that the placement puts elsewhere than
// on this node: P1 has no remote tools.
var ErrNotLocal = errors.New("tool: not placed on this node")

// DynamicFunc lists the tools that come and go while the node runs: the
// tools of the owner's modules (R-RUN-12). It is called on every lookup, so
// it must be quick and must not block.
type DynamicFunc func() []Handler

// noDynamic is the dynamic source of a registry with fixed tools only.
func noDynamic() []Handler { return nil }

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithDynamic adds the tools f lists after the fixed ones. A dynamic tool
// whose name is empty, a fixed tool's or an earlier dynamic one's is
// skipped and logged once: a module's tool never shadows a built-in one.
func WithDynamic(f DynamicFunc) RegistryOption { return func(r *Registry) { r.dynamic = f } }

// WithRegistryLogger sets where a skipped dynamic tool is reported. The
// default drops it.
func WithRegistryLogger(l *slog.Logger) RegistryOption { return func(r *Registry) { r.log = l } }

// Registry holds the tools by name: the fixed ones, registered at start,
// and the dynamic ones its DynamicFunc lists at each lookup.
type Registry struct {
	byName  map[string]Handler
	names   []string // sorted
	where   Placement
	dynamic DynamicFunc
	log     *slog.Logger

	mu     sync.Mutex
	warned map[string]bool // dynamic names already reported as skipped
}

// NewRegistry registers hs, which run where p puts them. A duplicate or
// empty name is an error.
func NewRegistry(p Placement, hs ...Handler) (*Registry, error) {
	return NewRegistryWith(p, nil, hs...)
}

// NewRegistryWith is NewRegistry with options, such as the dynamic tools
// (WithDynamic). The fixed tools stay positional, so NewRegistry's callers
// are unchanged.
func NewRegistryWith(p Placement, opts []RegistryOption, hs ...Handler) (*Registry, error) {
	r := &Registry{byName: make(map[string]Handler, len(hs)), where: p, dynamic: noDynamic,
		log: slog.New(slog.DiscardHandler), warned: map[string]bool{}}
	for _, o := range opts {
		o(r)
	}
	for _, h := range hs {
		name := h.Spec().Name
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("tool: register: empty tool name")
		}
		if _, dup := r.byName[name]; dup {
			return nil, fmt.Errorf("tool: register %s: duplicate name", name)
		}
		r.byName[name] = h
		r.names = append(r.names, name)
	}
	slices.Sort(r.names)
	return r, nil
}

// dynamicTools are the dynamic tools as the registry takes them, sorted by
// name: one whose name is empty or taken by a fixed tool or an earlier
// dynamic one is left out and reported once.
func (r *Registry) dynamicTools() []Handler {
	hs := r.dynamic()
	if len(hs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(hs))
	out := make([]Handler, 0, len(hs))
	for _, h := range hs {
		name := h.Spec().Name
		_, fixed := r.byName[name]
		if strings.TrimSpace(name) == "" || fixed || seen[name] {
			r.skipped(name)
			continue
		}
		seen[name] = true
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b Handler) int { return strings.Compare(a.Spec().Name, b.Spec().Name) })
	return out
}

// skipped reports a dynamic tool left out, once per name.
func (r *Registry) skipped(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warned[name] {
		return
	}
	r.warned[name] = true
	r.log.Warn("tool: a dynamic tool's name is empty or taken; it is left out", "tool", name)
}

// Specs lists the tools on offer by name, the fixed ones first; admin-only
// tools only for an admin, and a Gated tool only while it is offered.
func (r *Registry) Specs(admin bool) []Spec {
	dyn := r.dynamicTools()
	out := make([]Spec, 0, len(r.names)+len(dyn))
	for _, h := range slices.Concat(r.fixedTools(), dyn) {
		if s, ok := offered(h, admin); ok {
			out = append(out, s)
		}
	}
	return out
}

// fixedTools are the fixed tools in name order.
func (r *Registry) fixedTools() []Handler {
	out := make([]Handler, len(r.names))
	for i, name := range r.names {
		out[i] = r.byName[name]
	}
	return out
}

// Offered reports whether the tool called name is on offer to the
// caller, as Specs lists it.
func (r *Registry) Offered(name string, admin bool) bool {
	h, ok := r.Lookup(name)
	if !ok {
		return false
	}
	_, ok = offered(h, admin)
	return ok
}

// offered returns h's spec and whether h is on offer.
func offered(h Handler, admin bool) (Spec, bool) {
	s := h.Spec()
	if s.AdminOnly && !admin {
		return s, false
	}
	if g, ok := h.(Gated); ok && !g.Offered() {
		return s, false
	}
	return s, true
}

// Lookup returns the tool called name: a fixed one, else a dynamic one
// listed now.
func (r *Registry) Lookup(name string) (Handler, bool) {
	if h, ok := r.byName[name]; ok {
		return h, true
	}
	for _, h := range r.dynamicTools() {
		if h.Spec().Name == name {
			return h, true
		}
	}
	return nil, false
}

// Spec returns the spec of the tool called name, for whoever asks;
// false when there is none.
func (r *Registry) Spec(name string) (Spec, bool) {
	h, ok := r.Lookup(name)
	if !ok {
		return Spec{}, false
	}
	return h.Spec(), true
}

// Run runs the tool the call names. It is the end of the middleware
// chain, and it enforces the role itself: a name the model made up for a
// tool it was not offered still stops here (ErrUnknown, ErrForbidden).
// A tool the placement does not put on this node is refused with
// ErrNotLocal (R-RTR-3).
func (r *Registry) Run(ctx context.Context, c Call) (Result, error) {
	h, ok := r.Lookup(c.Name)
	if !ok {
		return Result{}, fmt.Errorf("tool: %s: %w", c.Name, ErrUnknown)
	}
	if h.Spec().AdminOnly && !c.Admin {
		return Result{}, fmt.Errorf("tool: %s: admin only: %w", c.Name, ErrForbidden)
	}
	if where := r.where.Where(c.Name); where != PlacementLocal {
		return Result{}, fmt.Errorf("tool: %s runs at %q: %w", c.Name, where, ErrNotLocal)
	}
	return h.Run(ctx, c)
}
