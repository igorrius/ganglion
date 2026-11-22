package modhost

import (
	"context"
	"maps"
	"slices"
	"sync"
)

// run is what a module's run carries in its context: who started it and
// how deep in a chain (ruling 3 of M4c). The host functions' ports read it:
// the cell port notes what the run wrote, so its listeners start when it
// ends (R-RUN-15). init and health runs carry none.
type run struct {
	ID      string   // the run's identity: the slot of a cron run, the call's key, the trigger event's id
	Trigger trigger  // cron | tool | cell | event | call
	Depth   int      // 0 outside a chain
	wrote   *written // the cells the run reported, full ids
	sent    *int     // messages notify sent so far in this run: the number in the last one's key
	emitted *int     // events emit sent so far in this run: the number in the last one's key
	calls   *counts  // the host calls the run made, by kind (R-RUN-20)

	// The cause the journal records (R-RUN-20): the tool's name for a tool
	// run; the producer module, its run and the cells for a cell run.
	tool        string
	producer    string
	producerRun string
	cells       []string
}

// newRun is a run that has written and sent nothing yet.
func newRun(id string, trig trigger, depth int) run {
	return run{ID: id, Trigger: trig, Depth: depth, wrote: &written{ids: map[string]bool{}}, sent: new(int), emitted: new(int),
		calls: &counts{n: map[string]int{}}}
}

// byTool is r as the run of the tool called name.
func (r run) byTool(name string) run {
	r.tool = name
	return r
}

// byEvent is r as the run a producer's run started by an event or a call,
// which names no cells.
func (r run) byEvent(producer, producerRun string) run {
	r.producer, r.producerRun = producer, producerRun
	return r
}

// byCells is r as the run a producer's run started by changing cells.
func (r run) byCells(producer, producerRun string, cells []string) run {
	r.producer, r.producerRun, r.cells = producer, producerRun, slices.Clone(cells)
	return r
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

// written is the set of cells one run reported. A run's host calls are
// sequential, but a module's calls may overlap: each run has its own set,
// so overlapping runs never share one. The lock keeps the set safe
// whichever goroutine a host call comes from.
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

// counts is the host calls one run made, by kind. The lock keeps it safe
// whichever goroutine a host call comes from.
type counts struct {
	mu sync.Mutex
	n  map[string]int
}

// add notes one host call of kind.
func (c *counts) add(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[kind]++
}

// copyOf is the counts so far; nil when there are none.
func (c *counts) copyOf() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.n) == 0 {
		return nil
	}
	return maps.Clone(c.n)
}
