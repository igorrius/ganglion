package modhost

import (
	"context"
	"slices"
	"sync"
)

// run is what a module's run carries in its context: who started it and
// how deep in a chain (ruling 3 of M4c). The host functions' ports read it:
// the cell port notes what the run wrote, so its listeners start when it
// ends (R-RUN-15). init and health runs carry none.
type run struct {
	ID      string   // the run's identity: the slot of a cron run, the call's key, the trigger event's id
	Trigger trigger  // cron | tool | cell
	Depth   int      // 0 outside a chain
	wrote   *written // the cells the run reported, full ids
	sent    *int     // notify calls so far in this run
}

// newRun is a run that has written and sent nothing yet.
func newRun(id string, trig trigger, depth int) run {
	return run{ID: id, Trigger: trig, Depth: depth, wrote: &written{ids: map[string]bool{}}, sent: new(int)}
}

// runKey is the context key of a run.
type runKey struct{}

// withRun is ctx carrying r.
func withRun(ctx context.Context, r run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

// runOf is the run ctx carries; false outside a run (init, health).
func runOf(ctx context.Context) (run, bool) {
	r, ok := ctx.Value(runKey{}).(run)
	return r, ok
}

// written is the set of cells a run reported. The runner serialises a
// module's calls, so one run writes it at a time; the lock keeps that from
// mattering.
type written struct {
	mu  sync.Mutex
	ids map[string]bool
}

// add notes that the run reported cell id.
func (w *written) add(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids[id] = true
}

// list is the cells reported, sorted.
func (w *written) list() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.ids))
	for id := range w.ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
