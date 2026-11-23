package natsbus

import (
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/bus"
)

// PortRef names a port of a module.
type PortRef struct{ Module, Port string }

// EdgeNeeds is what the links of the node's file ask of an edge's user,
// beyond what every edge has (ADR-0116). The core fills it from its links;
// it knows nothing of the file.
type EdgeNeeds struct {
	// Calls are the served ports of core modules that a module on the edge
	// calls: the edge may publish to each.
	Calls []PortRef
	// Serves are the served ports of modules on the edge that a core
	// module calls: the edge may subscribe to each.
	Serves []PortRef
	// Extra is added as it is, for what another part delivers to the edge
	// (the desired state of its modules).
	Extra UserPermissions
}

// EdgePermissions is the edge's user permissions (R-EDGE-2, ADR-0116): its
// own source deliveries, the replies of the core's JetStream requests, its
// telemetry and the API of its own domain, and for each link the one
// subject the link uses. The result is sorted and has no duplicates, so
// two derivations of the same links are equal. A name or port that is not
// one token is an error: the edge never gets a wildcard it was not given.
func EdgePermissions(edge string, n EdgeNeeds) (UserPermissions, error) {
	if err := bus.CheckEdgeName(edge); err != nil {
		return UserPermissions{}, err
	}
	tel, err := bus.Telemetry(edge)
	if err != nil {
		return UserPermissions{}, err
	}
	p := UserPermissions{
		Publish:   []string{edgeDeliver(edge) + ".>", "$JSC.R.>", tel},
		Subscribe: []string{edgeAPI(edge) + ".>"},
	}
	for _, c := range n.Calls {
		s, err := bus.Call(bus.CoreNode, c.Module, c.Port)
		if err != nil {
			return UserPermissions{}, fmt.Errorf("natsbus: edge %s: %w", edge, err)
		}
		p.Publish = append(p.Publish, s)
	}
	for _, c := range n.Serves {
		s, err := bus.Call(edge, c.Module, c.Port)
		if err != nil {
			return UserPermissions{}, fmt.Errorf("natsbus: edge %s: %w", edge, err)
		}
		p.Subscribe = append(p.Subscribe, s)
	}
	p.Publish = append(p.Publish, n.Extra.Publish...)
	p.Subscribe = append(p.Subscribe, n.Extra.Subscribe...)
	slices.Sort(p.Publish)
	slices.Sort(p.Subscribe)
	p.Publish = slices.Compact(p.Publish)
	p.Subscribe = slices.Compact(p.Subscribe)
	return p, nil
}
