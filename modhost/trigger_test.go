package modhost_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/tool"
)

// producerManifest is a module whose cron run reports two of its three
// cells and whose tool feed reports one.
const producerManifest = `name: probe
version: 1
triggers:
  - cron: "*/30 * * * *"
  - tool: feed
    about: Reports the charge.
    output: {ok: boolean, denied: boolean}
capabilities:
  state: true
cells:
  report:
    soc: {kind: sensor, type: integer, about: "Charge, %"}
    grid: {kind: sensor, type: integer, about: "Grid, W"}
    other: {kind: sensor, type: integer, about: "Other"}
config:
  op_cron: {type: string, default: '{"op":"cell_reports","json":{"soc":87,"grid":120}}'}
  op_tool_feed: {type: string, default: '{"op":"cell_report","name":"soc","json":5}'}
limits:
  timeout: 5s
`

// listenerManifest listens to the cells named by %s and reports its own x
// when its cell trigger runs the operation in op_cell.
func listenerManifest(cells string) string {
	return `name: probe
version: 1
triggers:
  - cell: [` + cells + `]
capabilities:
  state: true
cells:
  read: [` + cells + `]
  report:
    x: {kind: sensor, type: integer, about: "X"}
config:
  op_cell: {type: string, default: '{"op":"echo"}'}
escalate:
  - on: auth_failed
limits:
  timeout: 5s
`
}

// runCron runs module's cron trigger as the run of slot.
func (e *env) runCron(t *testing.T, module, slot string) {
	t.Helper()
	if err := e.host.RunCron(context.Background(), module, slot); err != nil {
		t.Fatal(err)
	}
}

// cellConsumerNames are the listeners' consumers on the bus.
func (ee *eventEnv) cellConsumerNames(t *testing.T) []string {
	t.Helper()
	names, err := ee.bus.ConsumerNames(context.Background(), bus.StreamEvents, "cells_")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// waitCellConsumers waits until exactly the consumers want exist on the
// bus.
func (ee *eventEnv) waitCellConsumers(t *testing.T, want ...string) {
	t.Helper()
	waitFor(t, "the listeners' consumers "+strings.Join(want, ","), func() bool { return slices.Equal(ee.cellConsumerNames(t), want) })
}

// cellRuns are the runs of module in the journal that a cells event started.
func (ee *eventEnv) cellRuns(module string) []modstore.Run {
	runs, _ := ee.store.Runs(context.Background(), module, 50)
	return slices.DeleteFunc(runs, func(r modstore.Run) bool { return r.Trigger != "cell" })
}

// publishCells puts a cells event of producer (a short name) on the bus as
// if its run run had written ids, at depth.
func (ee *eventEnv) publishCells(t *testing.T, producer, run, depth, id string, cells ...string) {
	t.Helper()
	payload, err := json.Marshal(map[string][]string{"cells": cells})
	if err != nil {
		t.Fatal(err)
	}
	err = ee.bus.Publish(context.Background(), "g.ev.core."+producer+".cells", id,
		map[string]string{bus.HeaderFrom: "core/local/" + producer, bus.HeaderRun: run, bus.HeaderDepth: depth}, payload)
	if err != nil {
		t.Fatal(err)
	}
}

// filtersOf are the subjects the listener's consumer reads.
func (ee *eventEnv) filtersOf(t *testing.T, short string) []string {
	t.Helper()
	c, err := ee.bus.JetStream().Consumer(context.Background(), bus.StreamEvents, "cells_"+short)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.Clone(info.Config.FilterSubjects)
}

// A run that wrote cells is one event of its module; every running module
// that listens to any of them runs once with the listened cells that
// changed, sorted, one level deeper, and the same run published twice
// starts nobody twice; a module listening to other cells runs nothing; a
// consumer reads only the cells of other modules (R-RUN-15, ADR-0108).
func TestACellChangeReachesTwoListeners(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc, p.grid")),
		active(t, "local/k", listenerManifest("p.soc")), active(t, "local/o", listenerManifest("p.other")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_k", "cells_l", "cells_o")
	ee.runCron(t, "local/p", "slot-1")
	ee.runCron(t, "local/p", "slot-1") // the cron event delivered again
	for _, m := range []string{"l", "k"} {
		waitFor(t, m+" to run", func() bool { return len(ee.cellRuns("local/"+m)) >= 1 })
	}
	waitFor(t, "o's consumer to take the change", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.cell.deliveries", map[string]string{"consumer": "cells_o", "outcome": "none"}) == 1
	})
	got, _ := ee.state.Get("l", "last_cell")
	if want := `{"cells":["p.grid","p.soc"],"producer":"local/p"}`; got != want {
		t.Errorf("l was given %s, want %s", got, want)
	}
	if got, _ := ee.state.Get("k", "last_cell"); got != `{"cells":["p.soc"],"producer":"local/p"}` {
		t.Errorf("k was given %s", got)
	}
	r := ee.cellRuns("local/l")
	if len(r) != 1 || r[0].Depth != 1 || r[0].Producer != "local/p" || r[0].ProducerRun != "slot-1" || r[0].Result != "ok" ||
		!slices.Equal(r[0].Cells, []string{"p.grid", "p.soc"}) || !strings.HasPrefix(r[0].ID, "cell:cells_l:") {
		t.Errorf("l's row: %+v", r)
	}
	if n := len(ee.cellRuns("local/o")); n != 0 {
		t.Errorf("o ran %d times for cells it does not listen to", n)
	}
	if got := ee.eventsOnStream(t); got != 1 {
		t.Errorf("EVENTS holds %d messages, want 1 for the run published twice", got)
	}
	if want := []string{"g.ev.core.p.cells"}; !slices.Equal(ee.filtersOf(t, "l"), want) {
		t.Errorf("l's consumer reads %v, want %v", ee.filtersOf(t, "l"), want)
	}
	if v := ee.rec.Value(t, "ganglion.modhost.cell.events", map[string]string{"module": "local/p", "outcome": "sent"}); v != 2 {
		t.Errorf("cells published = %v, want 2 (the second a duplicate the stream drops)", v)
	}
	waitFor(t, "the acks", func() bool { return ee.pendingOf(t, "cells_l") == 0 && ee.pendingOf(t, "cells_k") == 0 })
}

// A listener's consumer reads the cells events of the owners of the cells
// it listens to, and never its own module's, even when it writes cells
// itself.
func TestAListenerReadsTheCellsOfOtherModulesOnly(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/q", producerManifest),
		active(t, "local/l", listenerManifest("p.soc, q.grid, q.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	if want := []string{"g.ev.core.p.cells", "g.ev.core.q.cells"}; !slices.Equal(ee.filtersOf(t, "l"), want) {
		t.Fatalf("filters %v, want %v", ee.filtersOf(t, "l"), want)
	}
	ee.state.Put("l", "op_cell", `{"op":"cell_report","name":"x","json":1}`)
	ee.runCron(t, "local/p", "slot-1")
	waitFor(t, "l to run", func() bool { return len(ee.cellRuns("local/l")) == 1 })
	waitFor(t, "l's own cells to be published", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.cell.events", map[string]string{"module": "local/l", "outcome": "sent"}) == 1
	})
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, "cells_l") == 0 })
	if n := len(ee.cellRuns("local/l")); n != 1 {
		t.Fatalf("l ran %d times: its own cells started it", n)
	}
}

// A run that reports no cell publishes nothing.
func TestARunThatReportsNothingPublishesNothing(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.state.Put("p", "op_cron", `{"op":"echo"}`)
	ee.runCron(t, "local/p", "slot-1")
	if got := ee.eventsOnStream(t); got != 0 {
		t.Fatalf("EVENTS holds %d messages", got)
	}
}

// A listener that is off has no consumer, so it misses the changes of that
// time, and when it runs again it is not sent what it missed: it reads the
// values itself.
func TestAListenerThatIsOffMissesTheChangeAndIsNotReplayed(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	ctx := context.Background()
	if err := ee.host.SetOff(ctx, "l", true); err != nil {
		t.Fatal(err)
	}
	ee.waitCellConsumers(t) // deleted from the server with the module's stop
	ee.runCron(t, "local/p", "slot-1")
	if err := ee.host.SetOff(ctx, "l", false); err != nil {
		t.Fatal(err)
	}
	ee.waitCellConsumers(t, "cells_l")
	ee.runCron(t, "local/p", "slot-2")
	waitFor(t, "the change after it is on again", func() bool { return len(ee.cellRuns("local/l")) == 1 })
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, "cells_l") == 0 })
	if r := ee.cellRuns("local/l"); len(r) != 1 || r[0].ProducerRun != "slot-2" {
		t.Fatalf("l's runs: %+v, want only the change of slot-2", r)
	}
}

// The consumer follows the cells the module listens to: a new version that
// listens to cells of another module reads that module's cells events.
func TestAConsumerFollowsTheListenedCells(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/q", producerManifest),
		active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	ee.store.mu.Lock()
	ee.store.active[2] = active(t, "local/l", listenerManifest("p.soc, q.soc"))
	ee.store.mu.Unlock()
	if err := ee.host.Reload(context.Background(), "local/l"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the consumer to read q's cells", func() bool {
		return len(ee.cellConsumerNames(t)) == 1 && slices.Equal(ee.filtersOf(t, "l"), []string{"g.ev.core.p.cells", "g.ev.core.q.cells"})
	})
	ee.runCron(t, "local/q", "slot-1")
	waitFor(t, "l to run on q's cells", func() bool { return len(ee.cellRuns("local/l")) == 1 })
}

// A tool run is identified by its call's key, so a call run again after a
// restart publishes no second event.
func TestAToolRunPublishesUnderItsCallKey(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	for range 2 {
		if _, err := ee.tool("p.feed").Run(context.Background(), tool.Call{Name: "p.feed", Args: json.RawMessage(`{}`), Admin: true,
			Principal: owner, TurnID: "01TURN", N: 2}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "l to run", func() bool { return len(ee.cellRuns("local/l")) >= 1 })
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, "cells_l") == 0 })
	if r := ee.cellRuns("local/l"); len(r) != 1 || r[0].ProducerRun != "01TURN:2" || r[0].Depth != 1 || !slices.Equal(r[0].Cells, []string{"p.soc"}) {
		t.Fatalf("l's runs: %+v", r)
	}
}

// A cell run writes cells and starts the next listener one level deeper;
// past depth 4 the run starts nobody, and the node says so once for the
// build, however often the chain stops.
func TestAChainDeeperThanFourStops(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/l", listenerManifest("p.soc")), active(t, "local/m", listenerManifest("l.x")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l", "cells_m")
	ee.state.Put("l", "op_cell", `{"op":"cell_report","name":"x","json":1}`)
	ee.publishCells(t, "p", "slot", "3", "c3", "p.soc")
	waitFor(t, "m to refuse the cells of l at depth 4", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.cell.deliveries", map[string]string{"consumer": "cells_m", "outcome": "deep"}) == 1
	})
	if r := ee.cellRuns("local/l"); len(r) != 1 || r[0].Depth != 4 {
		t.Fatalf("l's runs: %+v, want one at depth 4", r)
	}
	ee.publishCells(t, "p", "slot", "4", "c4", "p.soc")
	ee.publishCells(t, "p", "slot", "4", "c5", "p.soc")
	waitFor(t, "l to refuse both", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.cell.deliveries", map[string]string{"consumer": "cells_l", "outcome": "deep"}) == 2 && ee.pendingOf(t, "cells_l") == 0
	})
	if n := len(ee.cellRuns("local/l")) + len(ee.cellRuns("local/m")); n != 1 {
		t.Fatalf("%d cell runs, want only l's at depth 4", n)
	}
	// l's and m's builds each told their owner once
	if n := strings.Count(ee.logText(), "deeper than 4"); n != 2 {
		t.Fatalf("the stopped chains were told %d times:\n%s", n, ee.logText())
	}
}

// A failing cell run is unattended, like cron: it escalates its kind once
// and counts towards the crash loop, and a module the node turned off loses
// its consumer (R-RUN-12, R-RUN-15).
func TestAFailingCellRunEscalatesAndCountsLikeCron(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	ee.state.Put("l", "op_cell", cronFails)
	for i := range 5 {
		ee.publishCells(t, "p", "slot", "0", "c"+string(rune('a'+i)), "p.soc")
	}
	ee.waitCellConsumers(t) // the crash loop stopped it, and its consumer went from the server
	var kinds []string
	for _, m := range ee.moduleEvents(t) {
		kinds = append(kinds, m.Kind)
	}
	if strings.Join(kinds, ",") != "auth_failed,module.crashloop" {
		t.Fatalf("escalations %v", kinds)
	}
	if !slices.Contains(ee.conf.offList(), "local/l:true:crashloop") {
		t.Fatalf("offs %v", ee.conf.offList())
	}
	if n := len(ee.store.failures()); n != 5 {
		t.Fatalf("%d failures counted", n)
	}
}

// A node that restarts keeps the listener's consumer: what was published
// while it was down runs once it is up, and the consumer stays the same.
func TestARestartKeepsTheListenersConsumer(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	if err := ee.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	ee.waitCellConsumers(t, "cells_l") // the node going down does not delete it
	ee.publishCells(t, "p", "slot-down", "0", "down", "p.soc")
	ee.host = ee.newHost()
	ee.start(t)
	waitFor(t, "the change made while down", func() bool { return len(ee.cellRuns("local/l")) == 1 })
	if r := ee.cellRuns("local/l"); r[0].ProducerRun != "slot-down" {
		t.Fatalf("l's runs: %+v", r)
	}
	ee.waitCellConsumers(t, "cells_l")
}

// A cells event of a producer that does not own the cells, or one the node
// cannot read, runs nothing.
func TestACellsEventOfSomeoneElsesCellsRunsNothing(t *testing.T) {
	ee := newModulesBusEnv(t, active(t, "local/l", listenerManifest("p.soc")))
	ee.start(t)
	ee.waitCellConsumers(t, "cells_l")
	ee.publishCells(t, "p", "slot", "0", "mine", "q.soc") // p says q's cell changed
	err := ee.bus.Publish(context.Background(), "g.ev.core.p.cells", "junk",
		map[string]string{bus.HeaderFrom: "core/local/p", bus.HeaderRun: "r", bus.HeaderDepth: "0"}, []byte("not json"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both to be taken", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.cell.deliveries", map[string]string{"consumer": "cells_l", "outcome": "none"}) == 1 &&
			ee.rec.Value(t, "ganglion.modhost.cell.deliveries", map[string]string{"consumer": "cells_l", "outcome": "bad"}) == 1 &&
			ee.pendingOf(t, "cells_l") == 0
	})
	if n := len(ee.cellRuns("local/l")); n != 0 {
		t.Fatalf("l ran %d times", n)
	}
}
