package telemetry

import (
	"slices"
	"sync"

	dto "github.com/prometheus/client_model/go"
)

// Network holds the latest metric families of each edge, already labelled
// with the edge's name, and gathers them for the core's /metrics (R-OBS-9).
// What it holds was checked by whoever set it.
type Network struct {
	mu    sync.Mutex
	nodes map[string][]*dto.MetricFamily
}

// NewNetwork returns an empty Network.
func NewNetwork() *Network { return &Network{nodes: map[string][]*dto.MetricFamily{}} }

// Set replaces what node sent.
func (n *Network) Set(node string, mfs []*dto.MetricFamily) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nodes[node] = mfs
}

// Forget drops node's series.
func (n *Network) Forget(node string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.nodes, node)
}

// Gather implements prometheus.Gatherer: one family per name, the nodes'
// metrics together, in the order of the names.
func (n *Network) Gather() ([]*dto.MetricFamily, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	byName := map[string]*dto.MetricFamily{}
	for _, mfs := range n.nodes {
		for _, mf := range mfs {
			have, ok := byName[mf.GetName()]
			if !ok {
				have = &dto.MetricFamily{Name: mf.Name, Help: mf.Help, Type: mf.Type, Unit: mf.Unit}
				byName[mf.GetName()] = have
			}
			if have.GetType() != mf.GetType() {
				continue // another version's family of another type: left out
			}
			have.Metric = append(have.Metric, mf.Metric...)
		}
	}
	out := make([]*dto.MetricFamily, 0, len(byName))
	for _, mf := range byName {
		out = append(out, mf)
	}
	slices.SortFunc(out, func(a, b *dto.MetricFamily) int {
		switch {
		case a.GetName() < b.GetName():
			return -1
		case a.GetName() > b.GetName():
			return 1
		}
		return 0
	})
	return out, nil
}
