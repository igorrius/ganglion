package tool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// Registry holds the tools by name.
type Registry struct {
	byName map[string]Handler
	names  []string // sorted
	where  Placement
}

// NewRegistry registers hs, which run where p puts them. A duplicate or
// empty name is an error.
func NewRegistry(p Placement, hs ...Handler) (*Registry, error) {
	r := &Registry{byName: make(map[string]Handler, len(hs)), where: p}
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

// Specs lists the tools by name; admin-only tools only for an admin.
func (r *Registry) Specs(admin bool) []Spec {
	out := make([]Spec, 0, len(r.names))
	for _, name := range r.names {
		if s := r.byName[name].Spec(); admin || !s.AdminOnly {
			out = append(out, s)
		}
	}
	return out
}

// Lookup returns the tool called name.
func (r *Registry) Lookup(name string) (Handler, bool) {
	h, ok := r.byName[name]
	return h, ok
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
