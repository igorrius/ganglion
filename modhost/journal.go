package modhost

import (
	"context"
	"slices"

	"github.com/igorrius/ganglion/modstore"
)

// This file is the run journal (R-RUN-20, ADR-0092): every run of a module
// is written to modstore's journal when it ends, with its cause, its host
// calls by kind, the cells it reported and the messages it sent, and, for a
// cell, event or call run, the chain of runs that led to it copied from the
// producer's row, and the link an event or a call came over. Never a module's text. Recording never fails or delays a run: a
// failed write is logged and counted.

// onCall is the runner's OnCall: it counts the call on the run ctx carries.
// init and health carry no run and are not counted.
func onCall(ctx context.Context, kind string) {
	if r, ok := runOf(ctx); ok {
		r.calls.add(kind)
	}
}

// record writes the row of the run r that was judged o. A failure to read
// the producer's row or to write this one is logged and counted; the run
// goes on.
func (h *Host) record(ctx context.Context, r ran, o outcome) {
	// The write does not end with ctx: the run happened, so a shutdown must
	// not lose its row.
	ctx = context.WithoutCancel(ctx)
	hop := modstore.Hop{Module: r.l.name, Version: r.l.version, Trigger: string(r.run.Trigger), Tool: r.run.tool, Link: r.run.link,
		Cells: slices.Clone(r.run.cells), At: r.at, Took: r.took, Result: string(o.result), Calls: r.run.calls.copyOf(),
		Wrote: r.run.wrote.list(), Sent: *r.run.sent}
	row := modstore.Run{
		Hop: hop, ID: r.run.ID, SHA256: r.l.sha, Producer: r.run.producer, ProducerRun: r.run.producerRun, Depth: r.run.Depth,
	}
	if o.failed() {
		row.Kind = string(o.kind)
	}
	if len(row.Wrote) == 0 {
		row.Wrote = nil
	}
	if r.run.Trigger == triggerCell || r.run.Trigger == triggerEvent || r.run.Trigger == triggerCall {
		row.Chain = h.chainOf(ctx, r.l.name, r.run)
	}
	if err := h.d.Store.RecordRun(ctx, row); err != nil {
		h.d.Log.Warn("modhost: a run was not recorded", "module", r.l.name, "err", err)
		h.in.journalFailed(ctx, r.l.name)
	}
}

// chainOf is the chain of runs that led to the run r: the producer's own
// chain and the producer's run, the last maxDepth hops. It is empty when the
// producer's row is not there yet (an event is published, and a call
// waits, while its producer's run goes on: the journal fills the chain
// when it reads the row), is gone, or cannot be read.
func (h *Host) chainOf(ctx context.Context, module string, r run) []modstore.Hop {
	p, ok, err := h.d.Store.Run(ctx, r.producer, r.producerRun)
	if err != nil {
		h.d.Log.Warn("modhost: the producer's run cannot be read; the chain is empty", "module", module, "producer", r.producer, "err", err)
		h.in.journalFailed(ctx, module)
		return nil
	}
	if !ok {
		return nil
	}
	chain := append(slices.Clone(p.Chain), p.Hop)
	return chain[max(len(chain)-maxDepth, 0):]
}
