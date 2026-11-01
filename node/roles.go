// Package node assembles a ganglion node from the roles enabled on it.
//
// One binary carries every role; flags decide which ones run. A single
// machine may run all three in one process (docs/spec.md, section 5).
package node

import (
	"fmt"
	"slices"
	"strings"
)

// Role is a part of the system a node can take on.
type Role string

const (
	// Core holds history, memory, the router, the tool registry and the
	// inbox/outbox.
	Core Role = "core"
	// Edge runs tools and WASM modules next to devices.
	Edge Role = "edge"
	// Worker executes models.
	Worker Role = "worker"
)

// allRoles lists every role in canonical order.
var allRoles = []Role{Core, Edge, Worker}

// ParseRoles parses a comma-separated role list such as "core,edge".
// Whitespace and duplicates are ignored; the result is in canonical order
// (core, edge, worker). An empty list or an unknown role is an error.
func ParseRoles(s string) ([]Role, error) {
	seen := make(map[Role]bool)
	for part := range strings.SplitSeq(s, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		r := Role(name)
		if !r.valid() {
			return nil, fmt.Errorf("unknown role %q (want one of core, edge, worker)", name)
		}
		seen[r] = true
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no roles given (want one or more of core, edge, worker)")
	}
	roles := make([]Role, 0, len(seen))
	for _, r := range allRoles {
		if seen[r] {
			roles = append(roles, r)
		}
	}
	return roles, nil
}

func (r Role) valid() bool {
	return slices.Contains(allRoles, r)
}
