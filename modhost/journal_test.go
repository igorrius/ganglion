package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/tool"
)

// runRow is the journal's row of module's run id, or fails the test.
func (e *env) runRow(t *testing.T, module, id string) modstore.Run {
	t.Helper()
	r, ok, err := e.store.Run(context.Background(), module, id)
	if err != nil || !ok {
		t.Fatalf("no run %s of %s in the journal (%v)", id, module, err)
	}
	return r
}

// R-RUN-20: a cron run that reports a cell is recorded with its cause, its
// host calls by kind and the cell it wrote.
func TestACronRunIsRecorded(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.cron(t, "")
	r := e.runRow(t, "local/probe", "slot")
	if r.Trigger != "cron" || r.Result != "ok" || r.Version != "3" || r.Depth != 0 || r.Took < 0 || !r.At.Equal(t0) {
		t.Fatalf("row %+v", r)
	}
	if r.Calls[runner.CallCellReport] != 1 || r.Calls[runner.CallNotify] != 0 || !slices.Equal(r.Wrote, []string{"probe.soc"}) || r.Sent != 0 || r.Kind != "" {
		t.Fatalf("calls %v, wrote %v, sent %d, kind %q", r.Calls, r.Wrote, r.Sent, r.Kind)
	}
	if len(r.Chain) != 0 || r.Producer != "" {
		t.Fatalf("a cron run has no chain: %+v", r)
	}
}

// A tool call is recorded as a tool run under the call's key, with the
// tool's name.
func TestAToolRunIsRecordedWithTheToolsName(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	call := tool.Call{Name: "probe.status", Args: json.RawMessage(`{}`), Admin: true, Principal: owner, TurnID: "01TURN", N: 1}
	if _, err := e.tool("probe.status").Run(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	r := e.runRow(t, "local/probe", "01TURN:1")
	if r.Trigger != "tool" || r.Tool != "probe.status" || r.Result != "ok" {
		t.Fatalf("row %+v", r)
	}
}

// A failed run keeps its result and its kind, never the module's text.
func TestAFailedRunRecordsItsResultAndKind(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.cron(t, cronFails)
	r := e.runRow(t, "local/probe", "slot")
	if r.Result != "failed" || r.Kind != "auth_failed" {
		t.Fatalf("row %+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), hostile) {
		t.Fatalf("the module's text is in the journal: %s", b)
	}
}

// A run started by a producer's cells records the producer, its run, the
// cells, the depth and a copy of the producer's chain: one hop for the
// producer's cron run, two for the next link.
func TestACellRunRecordsTheChainThatStartedIt(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc, p.grid")),
		active(t, "local/m", listenerManifest("l.x")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l", "cells_m")
	ee.state.Put("l", "op_cell", `{"op":"cell_report","name":"x","json":1}`)
	ee.runCron(t, "local/p", "slot-1")
	waitFor(t, "m to run", func() bool { return len(ee.cellRuns("local/m")) == 1 })
	prod := ee.runRow(t, "local/p", "slot-1")
	lr := ee.cellRuns("local/l")
	if len(lr) != 1 {
		t.Fatalf("l's runs %+v", lr)
	}
	l := lr[0]
	if l.Trigger != "cell" || l.Producer != "local/p" || l.ProducerRun != "slot-1" || l.Depth != 1 ||
		!slices.Equal(l.Cells, []string{"p.grid", "p.soc"}) || l.Calls[runner.CallCellReport] != 1 {
		t.Fatalf("row %+v", l)
	}
	if len(l.Chain) != 1 || l.Chain[0].Module != "local/p" || l.Chain[0].Trigger != "cron" ||
		!slices.Equal(l.Chain[0].Wrote, prod.Wrote) || l.Chain[0].Calls[runner.CallCellReport] != 2 {
		t.Fatalf("chain %+v, producer %+v", l.Chain, prod.Hop)
	}
	m := ee.cellRuns("local/m")[0]
	if m.Depth != 2 || len(m.Chain) != 2 || m.Chain[0].Module != "local/p" || m.Chain[1].Module != "local/l" || m.Producer != "local/l" || m.ProducerRun != l.ID {
		t.Fatalf("row %+v", m)
	}
}

// A producer whose row is gone leaves the chain empty; the row still names
// the producer.
func TestACellRunWithoutItsProducersRowHasNoChain(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	ee.publishCells(t, "p", "gone", "0", "c1", "p.soc")
	waitFor(t, "l to run", func() bool { return len(ee.cellRuns("local/l")) == 1 })
	if r := ee.cellRuns("local/l")[0]; len(r.Chain) != 0 || r.Producer != "local/p" || r.ProducerRun != "gone" {
		t.Fatalf("row %+v", r)
	}
}

// A module's message carries its run's id; the node's own lines carry none.
func TestAModulesMessageCarriesItsRunID(t *testing.T) {
	e := newEnv(t, active(t, "local/n", notifyManifest))
	e.start(t)
	e.runCron(t, "local/n", "slot-7")
	got := e.notifies(t)
	if len(got) != 1 || got[0].p.Run != "slot-7" {
		t.Fatalf("notifies %+v", got)
	}
	if r := e.runRow(t, "local/n", "slot-7"); r.Sent != 1 || r.Calls[runner.CallNotify] != 1 {
		t.Fatalf("row %+v", r)
	}
}

// A store that cannot record leaves the run settled and its cells
// published; the failure is logged and counted.
func TestARunIsSettledWhenItsRowCannotBeWritten(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.store.failRecord = errors.New("disk full")
	ee.runCron(t, "local/p", "slot-1")
	if n := ee.store.runs(); n != 1 {
		t.Fatalf("%d runs counted", n)
	}
	if got := ee.eventsOnStream(t); got != 1 {
		t.Fatalf("EVENTS holds %d messages, want the run's cells", got)
	}
	if !strings.Contains(ee.logText(), "a run was not recorded") {
		t.Fatalf("log:\n%s", ee.logText())
	}
	if v := ee.rec.Value(t, "ganglion.modhost.journal.failures", map[string]string{"module": "local/p"}); v != 1 {
		t.Errorf("journal failures = %v, want 1", v)
	}
}

// module's short or full name.
func TestTheCardHasTheLastRuns(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := e.host.RunCron(context.Background(), "local/probe", id); err != nil {
			t.Fatal(err)
		}
	}
	c, err := e.host.Card(context.Background(), "probe")
	if err != nil || len(c.Runs) != 3 || c.Runs[0].ID != "c" || c.Runs[2].ID != "a" {
		t.Fatalf("card runs %+v, %v", c.Runs, err)
	}
	if r, ok, err := e.host.Run(context.Background(), "probe", "b"); err != nil || !ok || r.ID != "b" {
		t.Fatalf("run: %+v, %v, %v", r, ok, err)
	}
}
