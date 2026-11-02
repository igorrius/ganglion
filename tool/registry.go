package tool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Registry holds the tools by name.
type Registry struct {
	byName map[string]Handler
	names  []string // sorted
}

// NewRegistry registers hs. A duplicate or empty name is an error.
func NewRegistry(hs ...Handler) (*Registry, error) {
	r := &Registry{byName: make(map[string]Handler, len(hs))}
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
func (r *Registry) Run(ctx context.Context, c Call) (Result, error) {
	h, ok := r.Lookup(c.Name)
	if !ok {
		return Result{}, fmt.Errorf("tool: %s: %w", c.Name, ErrUnknown)
	}
	if h.Spec().AdminOnly && !c.Admin {
		return Result{}, fmt.Errorf("tool: %s: admin only: %w", c.Name, ErrForbidden)
	}
	return h.Run(ctx, c)
}
