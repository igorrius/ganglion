package modhost

import (
	"context"
	"maps"
	"slices"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
)

// This file is the core's side of modules on edges (R-EDGE-6, ADR-0117): a
// module whose line names an edge is installed, rolled back and shown on
// the core like any other, but the core does not run it; it delivers the
// active version to the edge instead, and reads back what the edge runs.

// Delivery sends the modules whose lines name an edge to that edge
// (edge.Deliverer); nil on an edge, which delivers nothing.
type Delivery interface {
	// Deliver writes a's files and what edge should run: its version, its
	// digest and its config.
	Deliver(ctx context.Context, edge string, a modstore.Active, config map[string]any) error
	// Withdraw drops what any edge but keep should run of module, and,
	// when keep is "", its files.
	Withdraw(ctx context.Context, module, keep string) error
	// Actual is what edge says it runs of module; false when it has said
	// nothing.
	Actual(ctx context.Context, edge, module string) (modstore.EdgeRun, bool, error)
}

// edgeOf is the edge s sends its module to, "" when it runs on this node:
// a line naming this node itself runs here.
func (h *Host) edgeOf(s settings.Module) string {
	if e := s.OnEdge(); e != "" && e != h.d.Node {
		return e
	}
	return ""
}

// placeElsewhere notes module as running on edge ("" when it runs here). It
// holds h.mu.
func (h *Host) placeElsewhere(ctx context.Context, module, edge string) {
	h.stateMu.Lock()
	if edge == "" {
		delete(h.edges, module)
	} else {
		h.edges[module] = edge
	}
	h.stateMu.Unlock()
	if edge != "" {
		h.unload(ctx, module)
		h.setStopped(module, stop{}, false)
	}
}

// edgeOfModule is the edge module runs on, "" when it runs here.
func (h *Host) edgeOfModule(module string) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.edges[module]
}

// toEdge brings what the edges should run of module in line with the
// store and the settings: the active version to the edge its line names,
// nothing to every other edge. A failure is logged and noted for the
// module's line in /modules; the next reload or start tries again. It runs
// without h.mu: the bus may be slow.
func (h *Host) toEdge(ctx context.Context, module string) {
	if h.d.Delivery == nil {
		return
	}
	actives, mods, err := h.read(ctx)
	if err != nil {
		h.d.Log.Warn("modhost: a module is not delivered: the store or the settings cannot be read", "module", module, "err", err)
		return
	}
	var a *modstore.Active
	if i := slices.IndexFunc(actives, func(x modstore.Active) bool { return x.Module == module }); i >= 0 {
		a = &actives[i]
	}
	h.deliverOne(ctx, module, a, settingOf(mods, module))
}

// deliverOne is toEdge with the module's active version and line read.
func (h *Host) deliverOne(ctx context.Context, module string, a *modstore.Active, s settings.Module) {
	short := modstore.ShortName(module)
	e := h.edgeOf(s)
	if a == nil || s.Off || e == "" {
		h.noteDelivery(module, h.d.Delivery.Withdraw(ctx, short, ""))
		return
	}
	err := h.d.Delivery.Deliver(ctx, e, *a, declaredConfig(a.Manifest, s.Config))
	if err == nil {
		err = h.d.Delivery.Withdraw(ctx, short, e)
	}
	h.noteDelivery(module, err)
	if err == nil {
		h.d.Log.Info("modhost: module delivered to its edge", "module", module, "edge", e)
	}
}

// noteDelivery keeps how the last delivery of module went.
func (h *Host) noteDelivery(module string, err error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if err == nil {
		delete(h.undelivered, module)
		return
	}
	h.undelivered[module] = true
	h.d.Log.Warn("modhost: a module's delivery to its edge failed; the next reload or start tries again", "module", module, "err", err)
}

// declaredConfig is cfg cut to the keys the manifest declares: no other
// key reaches the edge.
func declaredConfig(manifest []byte, cfg map[string]any) map[string]any {
	m, err := runner.ParseManifest(manifest)
	if err != nil || len(cfg) == 0 {
		return nil
	}
	out := maps.Clone(cfg)
	maps.DeleteFunc(out, func(k string, _ any) bool { _, ok := m.Config[k]; return !ok })
	if len(out) == 0 {
		return nil
	}
	return out
}

// edgeRun is how module stands on its edge, for /modules: what the edge
// says it runs, that it has not answered for active (the digest of the
// version delivered), or that the delivery failed.
func (h *Host) edgeRun(ctx context.Context, module string, vs []modstore.Version) *modstore.EdgeRun {
	e := h.edgeOfModule(module)
	if e == "" {
		return nil
	}
	r := modstore.EdgeRun{Node: e}
	h.stateMu.Lock()
	undelivered := h.undelivered[module]
	h.stateMu.Unlock()
	switch {
	case undelivered:
		r.Result = modstore.EdgeUndelivered
	case h.d.Delivery == nil:
	default:
		got, ok, err := h.d.Delivery.Actual(ctx, e, modstore.ShortName(module))
		if err != nil {
			h.d.Log.Warn("modhost: what an edge runs cannot be read", "module", module, "edge", e, "err", err)
		}
		if ok {
			got.Node = e
			r = got
		}
		if i := slices.IndexFunc(vs, func(v modstore.Version) bool { return v.State == modstore.StateActive }); i >= 0 && r.Desired != vs[i].SHA256 {
			r.Result = modstore.EdgeWaiting
		}
	}
	return &r
}
