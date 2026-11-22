package modhost

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/pdk"
)

// This file is the cell triggers (R-RUN-15, ADR-0065): a run that wrote
// cells puts one module.trigger event per listening module into the inbox,
// and the agent hands each back to RunTrigger, with no model.

// maxDepth is the longest chain of modules a run's cells start: a run at
// this depth that writes cells starts nobody (M4c design section 4).
const maxDepth = links.MaxDepth

// triggerOutcome is what became of a run's cells, for the metrics.
type triggerOutcome string

// Trigger outcomes.
const (
	triggerPut     triggerOutcome = "put"     // a listener's event went to the inbox
	triggerFailed  triggerOutcome = "failed"  // the inbox refused it
	triggerStopped triggerOutcome = "stopped" // the chain is deeper than maxDepth
)

// cellInput is the payload of a cell trigger's run: the cells it listens
// to that changed, and who wrote them. The module reads the values itself.
type cellInput struct {
	Cells    []string `json:"cells"`
	Producer string   `json:"producer"`
}

// RunTrigger runs the listener of e once, as the run eventID, one level
// deeper in the chain than its producer's run, and settles it as an
// unattended run: counted, escalated, towards the crash loop (R-RUN-15).
// A module that is not running is a no-op: the event is acknowledged and
// dropped. A failed run is no error; the node's failure to disable a
// crash-looping module is logged, since running the event again would run
// the module again. An error is a run that could not start because ctx
// ended, so the event is delivered again.
func (h *Host) RunTrigger(ctx context.Context, e event.ModuleTrigger, eventID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(cellInput{Cells: e.Cells, Producer: e.Producer})
	if err != nil {
		return err
	}
	depth := max(e.Depth, 1) // a trigger's run is in a chain, so the cap holds whatever the event says
	r, ok := h.call(ctx, e.Module, pdk.Input{Trigger: string(triggerCell), Payload: payload}, newRun(eventID, triggerCell, depth).byCells(e.Producer, e.Run, e.Cells))
	if !ok {
		h.d.Log.Info("modhost: the cell trigger of a module that is not running; dropped", "module", e.Module, "producer", e.Producer)
		return nil
	}
	if _, err := h.finish(ctx, r, judge(r.out, r.err, r.l.short+" cell")); err != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", err)
	}
	return nil
}

// fanOut puts one module.trigger per running listener of the cells r
// wrote, keyed by listener, producer and run, so a run delivered again
// starts nobody twice. Past maxDepth it puts none.
func (h *Host) fanOut(ctx context.Context, l *loaded, r run) {
	cells := r.wrote.list()
	if len(cells) == 0 {
		return
	}
	if r.Depth+1 > maxDepth {
		h.chainStopped(ctx, l)
		return
	}
	for _, sub := range h.listeners(l.name, cells) {
		h.putTrigger(ctx, l.name, sub, r)
	}
}

// listener is a running module and the cells it listens to among those a
// run wrote.
type listener struct {
	l     *loaded
	cells []string
}

// listeners are the running modules other than producer that listen to any
// of cells, in name order. A module that is off, crash-looped or removed is
// not running, so it gets no event.
func (h *Host) listeners(producer string, cells []string) []listener {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []listener
	for name, l := range h.running {
		if name == producer {
			continue
		}
		var hit []string
		for _, id := range l.m.Listens() {
			if slices.Contains(cells, id) {
				hit = append(hit, id)
			}
		}
		if len(hit) > 0 {
			out = append(out, listener{l: l, cells: hit})
		}
	}
	slices.SortFunc(out, func(a, b listener) int { return strings.Compare(a.l.name, b.l.name) })
	return out
}

// putTrigger puts the event that runs sub for the run r of producer, in the
// conversation module:<short name>, so the queue runs one of sub's events
// at a time (ruling 4 of M4c). The put does not end with ctx: the producer
// ran, so a shutdown must not lose its trigger. A failure to put it is
// logged: a lost trigger never fails the producer's run.
func (h *Host) putTrigger(ctx context.Context, producer string, sub listener, r run) {
	now := h.d.Now()
	payload, err := json.Marshal(event.ModuleTrigger{Module: sub.l.name, Producer: producer, Run: r.ID, Cells: sub.cells, Depth: r.Depth + 1})
	if err == nil {
		err = h.d.Inbox.Put(context.WithoutCancel(ctx), event.Event{ID: event.NewID(now), IdemKey: "trigger:" + sub.l.name + ":" + producer + ":" + r.ID,
			Source: event.SourceModule, Conv: "module:" + sub.l.short, Kind: event.KindModuleTrigger, Payload: payload, At: now})
	}
	if err != nil {
		h.in.triggered(ctx, producer, triggerFailed)
		h.d.Log.Error("modhost: a cell trigger was not put", "module", sub.l.name, "producer", producer, "err", err)
		return
	}
	h.in.triggered(ctx, producer, triggerPut)
}

// chainStopped is a run of l deeper than maxDepth that wrote cells: no
// event is put. The node says so in its log and to l's owner, in a node
// line, once per active build, in memory (ruling 6 of M4c): after a restart
// the log may say it once more, while the line's key keeps the owner's line
// to one as long as the inbox holds it. With loops refused at install, this
// is a backstop.
func (h *Host) chainStopped(ctx context.Context, l *loaded) {
	h.in.triggered(ctx, l.name, triggerStopped)
	if !h.firstStop(l.sha) {
		return
	}
	h.d.Log.Warn("modhost: a chain of modules deeper than 4 stopped; its run's cells start no module",
		"module", l.name, "version", l.version, "sha", l.sha)
	line := event.ModuleNotify{Node: event.NotifyChainLine, PerDay: perDay(l)}
	if err := h.putNotify(ctx, l, "notify:"+l.name+":chain:"+l.sha, line, h.d.Now()); err != nil {
		h.d.Log.Error("modhost: the line of a stopped chain was not put", "module", l.name, "err", err)
	}
}

// firstStop reports whether the build sha's chain stops for the first time
// since the node started, and remembers it.
func (h *Host) firstStop(sha string) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.stops[sha] {
		return false
	}
	h.stops[sha] = true
	return true
}
