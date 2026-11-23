// Package edge is the edge role's telemetry (R-EDGE-7, R-OBS-9, ADR-0115):
// on an edge, a Publisher sends a snapshot of the node every
// edge.telemetry_every and at once when its link comes up; on the core, a
// Collector keeps the latest snapshot of each edge, serves the edges'
// metrics beside the core's own, gets an edge's kept events copied again
// when it is heard after a silence, and gives /status a line per edge. It
// imports no NATS package: the bus is reached through functions.
package edge

import (
	"encoding/json"
	"errors"
	"time"
)

// Version is the snapshot format this node writes.
const Version = 1

// MaxSnapshot is the largest snapshot the core takes, and the edge sends;
// an edge whose metrics do not fit sends the snapshot without them.
const MaxSnapshot = 256 << 10

// Snapshot is what an edge says of itself on g.tel.<edge>. It names no
// edge: the core takes the name from the subject.
type Snapshot struct {
	V  int       `json:"v"`
	At time.Time `json:"at"`
	// Every is the edge's edge.telemetry_every, so the core knows when it is
	// late.
	Every time.Duration `json:"every"`
	// Linked is the edge's own view of its link.
	Linked bool `json:"linked"`
	// Relinked is true on the first snapshot after the link came up.
	Relinked bool `json:"relinked,omitempty"`
	// Events is how many events its stream holds.
	Events uint64 `json:"events"`
	// Modules are the modules it runs.
	Modules []Module `json:"modules"`
	// Metrics are the edge's metrics in the Prometheus text format.
	Metrics string `json:"metrics,omitempty"`
}

// Module is one module an edge runs.
type Module struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ErrSnapshot is a snapshot the core does not take.
var ErrSnapshot = errors.New("edge: not a snapshot")

// The interval the core believes, whatever the edge claims: an edge cannot
// hide its silence behind a long one.
const (
	minEvery = 5 * time.Second
	maxEvery = 5 * time.Minute
)

// decode reads a snapshot, refusing one too large, of another version, or
// with no interval, and clamps the interval.
func decode(data []byte) (Snapshot, error) {
	var s Snapshot
	if len(data) > MaxSnapshot || json.Unmarshal(data, &s) != nil || s.V != Version || s.Every <= 0 {
		return Snapshot{}, ErrSnapshot
	}
	s.Every = min(max(s.Every, minEvery), maxEvery)
	return s, nil
}
