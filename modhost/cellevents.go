package modhost

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// This file is how cell triggers travel (R-RUN-15, R-BUS-6, ADR-0108, which
// keeps the rules of ADR-0065): a run that wrote cells publishes one event
// on its module's cells output, and every running module that listens to
// any of them has one durable consumer on EVENTS that runs it, with no
// model.

// cellsPrefix starts the name of every listener's consumer: cells_<short>.
const cellsPrefix = "cells_"

// cellOutcome is what became of the publishing of a run's cells, for the
// metrics.
type cellOutcome string

// Cell outcomes.
const (
	cellSent   cellOutcome = "sent"
	cellFailed cellOutcome = "failed" // the node could not publish it
)

// cellDeliveryOutcome is what became of a cells event a listener's consumer
// handled, for the metrics.
type cellDeliveryOutcome string

// Cell delivery outcomes.
const (
	cellRan  cellDeliveryOutcome = "ok"   // the module ran (whatever its result: a cell run is not repeated)
	cellNone cellDeliveryOutcome = "none" // none of the cells is one the module listens to
	cellDeep cellDeliveryOutcome = "deep" // the chain is too deep; acked, nothing ran
	cellGone cellDeliveryOutcome = "gone" // the module is not running; acked, nothing ran
	cellBad  cellDeliveryOutcome = "bad"  // not a cells event; acked
)

// cellsEvent is the payload of a module's event on its cells output: the
// ids of the cells its run wrote, sorted. Never a value (R-RUN-20).
type cellsEvent struct {
	Cells []string `json:"cells"`
}

// publishCells publishes the cells the run r wrote, once the run is settled
// and recorded, so the producer's journal row exists before a listener's
// row names it. The key is the module and the run, so a run published
// again within the stream's window starts nobody twice. The publish does not
// end with ctx: the producer ran, so a shutdown must not lose its cells. A
// failure is logged: a lost event never fails the producer's run.
func (h *Host) publishCells(ctx context.Context, l *loaded, r run) {
	cells := r.wrote.list()
	if len(cells) == 0 {
		return
	}
	subject, err := bus.Event(eventNode, l.short, runner.CellsOutput)
	if err == nil {
		var payload []byte
		if payload, err = json.Marshal(cellsEvent{Cells: cells}); err == nil {
			headers := map[string]string{bus.HeaderFrom: fromNode + "/" + l.name, bus.HeaderRun: r.ID, bus.HeaderDepth: strconv.Itoa(r.Depth)}
			err = h.d.Publisher.Publish(context.WithoutCancel(ctx), subject, "cells:"+l.name+":"+r.ID, headers, payload)
		}
	}
	if err != nil {
		h.in.cellsPublished(ctx, l.name, cellFailed)
		h.d.Log.Error("modhost: the cells of a run were not published", "module", l.name, "err", err)
		return
	}
	h.in.cellsPublished(ctx, l.name, cellSent)
}

// wantedCells is a listener's consumer that should run.
type wantedCells struct {
	short string
	spec  bus.ConsumerSpec
}

// runningCells is a listener consumer's goroutine.
type runningCells struct {
	spec   bus.ConsumerSpec
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the goroutine has returned
	drop   atomic.Bool   // the module stopped running: the goroutine deletes the consumer from the server as it ends
}

// cellFilters are the subjects of the cells events of the modules that own
// the cells ids, sorted, other than the module short itself: a module's own
// cells never start it. The owner of a cell is the first part of its id.
func cellFilters(short string, ids []string) []string {
	var out []string
	for _, id := range ids {
		owner, _, _ := strings.Cut(id, ".")
		if owner == short {
			continue
		}
		subject, err := bus.Event(eventNode, owner, runner.CellsOutput)
		if err != nil || slices.Contains(out, subject) {
			continue
		}
		out = append(out, subject)
	}
	slices.Sort(out)
	return out
}

// wantedCellConsumers are the consumers that should run, by name: one for
// each running module that listens to cells of another module's.
func (h *Host) wantedCellConsumers() map[string]wantedCells {
	h.mu.RLock()
	defer h.mu.RUnlock()
	want := map[string]wantedCells{}
	for _, l := range h.running {
		filters := cellFilters(l.short, l.m.Listens())
		if len(filters) == 0 {
			continue
		}
		name := cellsPrefix + l.short
		want[name] = wantedCells{short: l.short, spec: bus.ConsumerSpec{Stream: bus.StreamEvents, Name: name, Description: "the cells " + l.short + " listens to",
			Filters: filters, Deliver: bus.DeliverNew, AckWait: l.m.Limits.Timeout + linkAckSlack, Retry: linkRetry}}
	}
	return want
}

// syncCells brings the listeners' consumers in line with the modules that
// run: it stops those no longer wanted or changed (waiting for them), starts
// the missing ones and deletes from the server the consumers of modules that
// do not run. A listener that does not run misses the changes of that time;
// it reads the cells' values when it runs again. The caller holds the
// reconcile's lock.
func (h *Host) syncCells(ctx context.Context) {
	want := h.wantedCellConsumers()
	for _, done := range h.links.retireCells(want) {
		<-done
	}
	h.links.startCells(h, want)
	names, err := h.d.Consumers.ConsumerNames(ctx, bus.StreamEvents, cellsPrefix)
	if err != nil {
		h.d.Log.Warn("modhost: the listeners' consumers on the bus cannot be listed", "err", err)
		return
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			continue
		}
		if err := h.d.Consumers.DeleteConsumer(ctx, bus.StreamEvents, n); err != nil {
			h.d.Log.Warn("modhost: the consumer of a module that does not run is not deleted", "consumer", n, "err", err)
		}
	}
}

// retireCells cancels the listeners' consumers that are no longer wanted,
// whose settings changed, or whose context already ended, and returns what
// to wait for.
func (s *linkSet) retireCells(want map[string]wantedCells) []chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []chan struct{}
	for name, c := range s.cells {
		w, ok := want[name]
		if !ok || w.spec.AckWait != c.spec.AckWait || !slices.Equal(w.spec.Filters, c.spec.Filters) || c.ctx.Err() != nil {
			c.cancel()
			out = append(out, c.done)
		}
	}
	return out
}

// startCells runs the wanted consumers that do not run yet, unless the host
// was shut down.
func (s *linkSet) startCells(h *Host, want map[string]wantedCells) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	for name, w := range want {
		if _, ok := s.cells[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		c := &runningCells{spec: w.spec, ctx: ctx, cancel: cancel, done: make(chan struct{})}
		s.cells[name] = c
		s.wg.Add(1)
		go h.runCellsConsumer(name, w, c)
	}
}

// runCellsConsumer is one listener consumer's goroutine.
func (h *Host) runCellsConsumer(name string, w wantedCells, c *runningCells) {
	defer h.links.wg.Done()
	defer func() {
		if c.drop.Load() {
			// It ended with no ctx of its own: the module is not running, so
			// nothing of its is to wait for it on the server.
			if err := h.d.Consumers.DeleteConsumer(context.WithoutCancel(c.ctx), bus.StreamEvents, name); err != nil {
				h.d.Log.Warn("modhost: the consumer of a module that does not run is not deleted", "consumer", name, "err", err)
			}
		}
		h.links.mu.Lock()
		if h.links.cells[name] == c {
			delete(h.links.cells, name)
		}
		h.links.mu.Unlock()
		close(c.done)
	}()
	h.consume(c.ctx, c.spec, "the cells of "+w.short, func(ctx context.Context, d bus.Delivery) error {
		return h.deliverCells(ctx, name, w.short, d)
	})
}

// stopCells cancels the consumer of the module short (its short name) and
// has it deleted from the server as it ends, without waiting: it is called
// from a run's own consumer, which must not wait for itself.
func (h *Host) stopCells(short string) {
	h.links.mu.Lock()
	defer h.links.mu.Unlock()
	if c, ok := h.links.cells[cellsPrefix+short]; ok {
		c.drop.Store(true)
		c.cancel()
	}
}

// deliverCells is the handler of a listener's consumer: it intersects the
// cells of the event with the ones the module listens to and runs the
// module once with them, one level deeper in the chain than the producer's
// run, as an unattended run (counted, escalated, towards the crash loop;
// R-RUN-15). Whatever the run's result, the event is acked: a cell run is
// not repeated, the next change starts it again. It is left unacked only
// when ctx ended before the run could start: the node is shutting down, or
// the consumer was stopped.
func (h *Host) deliverCells(ctx context.Context, name, short string, d bus.Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.RLock()
	l, ok := h.running[fullName(short)]
	h.mu.RUnlock()
	if !ok {
		h.in.cellsDelivered(ctx, name, cellGone)
		h.d.Log.Info("modhost: the cells of a module that is not running; dropped", "module", fullName(short))
		return nil
	}
	var ev cellsEvent
	if err := json.Unmarshal(d.Payload, &ev); err != nil {
		h.in.cellsDelivered(ctx, name, cellBad)
		h.d.Log.Warn("modhost: a cells event the node cannot read; dropped", "module", l.name, "err", err)
		return nil
	}
	producer := strings.TrimPrefix(d.Headers[bus.HeaderFrom], fromNode+"/")
	hit := listened(l, producer, ev.Cells)
	if len(hit) == 0 {
		h.in.cellsDelivered(ctx, name, cellNone)
		return nil
	}
	depth, _ := strconv.Atoi(d.Headers[bus.HeaderDepth])
	if depth >= maxDepth {
		h.in.cellsDelivered(ctx, name, cellDeep)
		h.chainLine(ctx, l, "a chain of modules deeper than 4 stopped; its run's cells start no module")
		return nil
	}
	return h.runCells(ctx, name, d, depth, producer, hit)
}

// listened are the cells of ids that l listens to and that producer, a full
// name, owns: the bus's data is not trusted to name a cell of someone else.
// A module's own cells are never among them.
func listened(l *loaded, producer string, ids []string) []string {
	if producer == l.name {
		return nil
	}
	own := modstore.ShortName(producer) + "."
	var hit []string
	for _, id := range l.m.Listens() {
		if slices.Contains(ids, id) && strings.HasPrefix(id, own) {
			hit = append(hit, id)
		}
	}
	return hit
}

// runCells runs the listener with the cells that changed and settles the
// run. The run does not end with ctx: stopping a consumer must not fail a
// run that is half done, and the module's own time limit bounds it.
func (h *Host) runCells(ctx context.Context, name string, d bus.Delivery, depth int, producer string, hit []string) error {
	payload, err := json.Marshal(cellInput{Cells: hit, Producer: producer})
	if err != nil {
		return err
	}
	id := "cell:" + name + ":" + strconv.FormatUint(d.Seq, 10)
	if d.Tries > 1 {
		id += ":" + strconv.FormatUint(d.Tries, 10) // a run that started again after a crash is its own row in the journal
	}
	module := fullName(strings.TrimPrefix(name, cellsPrefix))
	r, ok := h.call(context.WithoutCancel(ctx), module, pdk.Input{Trigger: string(triggerCell), Payload: payload},
		newRun(id, triggerCell, depth+1).byCells(producer, d.Headers[bus.HeaderRun], hit))
	if !ok {
		h.in.cellsDelivered(ctx, name, cellGone)
		h.d.Log.Info("modhost: the cells of a module that is not running; dropped", "module", module, "producer", producer)
		return nil
	}
	if _, err := h.finish(ctx, r, judge(r.out, r.err, r.l.short+" cell")); err != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", err)
	}
	h.in.cellsDelivered(ctx, name, cellRan)
	return nil
}
