package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
)

// This file is the health of a module (R-RUN-9, R-RUN-12, ruling 3 of M4b):
// a build's, in a throwaway instance before it is installed, and a running
// module's, on its card.

// ErrCheckFailed is a build that did not pass the check before its install:
// it does not load, or its health trigger failed. The error names which in
// the node's words, never the module's.
var ErrCheckFailed = errors.New("the build did not pass its check")

// errCheckReport refuses a cell report from the throwaway instance: a check
// changes nothing of the node.
var errCheckReport = errors.New("modhost: a build under check reports no cell")

// Check loads build b in a throwaway instance as it would run (its
// manifest's limits, the node's secrets but never the node's own, config
// values from cfg) with a state that is dropped and cells it may read but
// not report, runs its health trigger and closes it. Nothing of the node
// changes. An ErrCheckFailed names what failed in the node's words; the
// module's own text goes to the log only (R-RUN-9, R-RUN-14).
func (h *Host) Check(ctx context.Context, b modstore.Build, cfg map[string]any) error {
	m, err := runner.ParseManifest(b.Manifest)
	if err != nil {
		h.d.Log.Warn("modhost: a build's manifest does not load", "module", b.Module, "err", err)
		return fmt.Errorf("%w: its manifest does not load", ErrCheckFailed)
	}
	m.ConfigValues = configValues(m, cfg)
	short := modstore.ShortName(b.Module)
	mod, err := runner.Load(ctx, b.Wasm, m, runner.Deps{Secrets: h.secret(b.Module), State: runner.NewMemState(),
		Cells: checkCells{cellPort{cells: h.d.Cells, module: b.Module, prefix: short}}, Prefix: short, Logger: h.d.Log})
	if err != nil {
		h.d.Log.Warn("modhost: a build does not load", "module", b.Module, "err", err)
		return fmt.Errorf("%w: it does not load on this node", ErrCheckFailed)
	}
	defer func() { _ = mod.Close(context.WithoutCancel(ctx)) }()
	if err := mod.Health(ctx); err != nil {
		k := lifecycleKind(err)
		h.d.Log.Warn("modhost: a build's health check failed", "module", b.Module, "kind", string(k), "err", err)
		return fmt.Errorf("%w: its health check failed (%s)", ErrCheckFailed, k)
	}
	return nil
}

// checkCells is the cell port of a build under check: it reads as the
// module would, and refuses every report.
type checkCells struct{ cellPort }

// Report implements runner.CellPort: refused.
func (checkCells) Report(context.Context, string, json.RawMessage, string) error {
	return errCheckReport
}

// lifecycleKind is the kind an init or health failure is told by: the kind
// the module reported when it may report it, else KindException.
func lifecycleKind(err error) Kind {
	if me, ok := errors.AsType[*runner.ModuleError](err); ok {
		return moduleKind(me.Kind)
	}
	return KindException
}

// health runs the health trigger of module while it runs: HealthOK, or the
// kind it failed with; "" when the module is not running.
func (h *Host) health(ctx context.Context, module string) modstore.Health {
	h.mu.RLock()
	defer h.mu.RUnlock()
	l, ok := h.running[module]
	if !ok {
		return ""
	}
	if err := l.mod.Health(ctx); err != nil {
		k := lifecycleKind(err)
		h.d.Log.Warn("modhost: a module's health check failed", "module", module, "version", l.version, "kind", string(k), "err", err)
		return modstore.Health(k)
	}
	return modstore.HealthOK
}
