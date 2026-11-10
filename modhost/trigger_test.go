package modhost_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
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

// triggers are the module.trigger events in the inbox, decoded, each
// checked for the node's source and its listener's conversation.
func (e *env) triggers(t *testing.T) []event.ModuleTrigger {
	t.Helper()
	var out []event.ModuleTrigger
	for _, ev := range e.inbox.taken() {
		if ev.Kind != event.KindModuleTrigger {
			continue
		}
		var m event.ModuleTrigger
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			t.Fatal(err)
		}
		short := strings.TrimPrefix(m.Module, "local/")
		if ev.Source != event.SourceModule || ev.Conv != "module:"+short || ev.IdemKey != "trigger:"+m.Module+":"+m.Producer+":"+m.Run {
			t.Fatalf("event %+v", ev)
		}
		out = append(out, m)
	}
	return out
}

// runCron runs module's cron trigger as the run of slot.
func (e *env) runCron(t *testing.T, module, slot string) {
	t.Helper()
	if err := e.host.RunCron(context.Background(), module, slot); err != nil {
		t.Fatal(err)
	}
}

// A producer's run that wrote cells puts one event for each running module
// that listens to any of them, with the listened cells sorted and depth 1;
// a module listening to other cells gets none, and the same run twice puts
// one (R-RUN-15, ADR-0065).
func TestARunPutsOneTriggerPerListener(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc, p.grid")),
		active(t, "local/o", listenerManifest("p.other")))
	e.start(t)
	e.runCron(t, "local/p", "cron:module:local/p:0:1")
	e.runCron(t, "local/p", "cron:module:local/p:0:1") // the event delivered again
	got := e.triggers(t)
	want := event.ModuleTrigger{Module: "local/l", Producer: "local/p", Run: "cron:module:local/p:0:1",
		Cells: []string{"p.grid", "p.soc"}, Depth: 1}
	if len(got) != 1 || !sameTrigger(got[0], want) {
		t.Fatalf("triggers %+v, want one %+v", got, want)
	}
	e.runCron(t, "local/p", "cron:module:local/p:0:2")
	if got := e.triggers(t); len(got) != 2 || got[1].Run != "cron:module:local/p:0:2" {
		t.Fatalf("the next slot: %+v", got)
	}
	// three puts, the queue keeping the one delivered again once
	if v := rec.Value(t, "ganglion.modhost.triggers", map[string]string{"module": "local/p", "outcome": "put"}); v != 3 {
		t.Errorf("triggers put = %v, want 3", v)
	}
}

func sameTrigger(a, b event.ModuleTrigger) bool {
	return a.Module == b.Module && a.Producer == b.Producer && a.Run == b.Run && slices.Equal(a.Cells, b.Cells) && a.Depth == b.Depth
}

// A run that reports no cell starts nobody.
func TestARunThatReportsNothingPutsNothing(t *testing.T) {
	e := newEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	e.start(t)
	e.state.Put("p", "op_cron", `{"op":"echo"}`)
	e.runCron(t, "local/p", "slot-1")
	if got := e.inbox.taken(); len(got) != 0 {
		t.Fatalf("events %+v", got)
	}
}

// A listener the settings turn off is not running and gets no event.
func TestAnOffListenerGetsNoEvent(t *testing.T) {
	e := newEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	e.conf.mods = []settings.Module{{Name: "local/l", Off: true}}
	e.start(t)
	e.runCron(t, "local/p", "slot-1")
	if got := e.inbox.taken(); len(got) != 0 {
		t.Fatalf("events %+v", got)
	}
}

// A tool run is identified by its call's key, so a call run again after a
// restart puts no second event.
func TestAToolRunTriggersUnderItsCallKey(t *testing.T) {
	e := newEnv(t, active(t, "local/p", producerManifest), active(t, "local/l", listenerManifest("p.soc")))
	e.start(t)
	for range 2 {
		if _, err := e.tool("p.feed").Run(context.Background(), tool.Call{Name: "p.feed", Args: json.RawMessage(`{}`), Admin: true,
			Principal: owner, TurnID: "01TURN", N: 2}); err != nil {
			t.Fatal(err)
		}
	}
	got := e.triggers(t)
	if len(got) != 1 || got[0].Run != "01TURN:2" || !slices.Equal(got[0].Cells, []string{"p.soc"}) || got[0].Depth != 1 {
		t.Fatalf("triggers %+v", got)
	}
}

// RunTrigger runs the listener with the cell trigger's input: the cells
// and the producer; the listener's run is a run like any other. A module
// that is not running is no work and no error.
func TestRunTriggerPassesTheCellsAndTheProducer(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/l", listenerManifest("p.soc, p.grid")))
	e.start(t)
	trig := event.ModuleTrigger{Module: "local/l", Producer: "local/p", Run: "slot-1", Cells: []string{"p.grid", "p.soc"}, Depth: 1}
	if err := e.host.RunTrigger(context.Background(), trig, "01EVTRIGGER"); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.state.Get("l", "last_cell"); v != `{"cells":["p.grid","p.soc"],"producer":"local/p"}` {
		t.Fatalf("the module got %q", v)
	}
	if got := rec.Value(t, "ganglion.modhost.runs", map[string]string{"module": "local/l", "version": "1", "trigger": "cell", "result": "ok"}); got != 1 {
		t.Fatalf("cell runs = %v", got)
	}
	trig.Module = "local/gone"
	if err := e.host.RunTrigger(context.Background(), trig, "01EVTRIGGER2"); err != nil {
		t.Fatalf("a module that is not running: %v", err)
	}
	if n := e.store.runs(); n != 1 {
		t.Fatalf("%d runs counted", n)
	}
}

// A trigger run writes cells and starts the next link one level deeper;
// past depth 4 no event is put, and the node says so once for the build,
// however often the chain stops (ruling 6).
func TestAChainDeeperThanFourStops(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/l", listenerManifest("p.soc")), active(t, "local/m", listenerManifest("l.x")))
	e.start(t)
	e.state.Put("l", "op_cell", `{"op":"cell_report","name":"x","json":1}`)
	run := func(depth int, id string) {
		t.Helper()
		trig := event.ModuleTrigger{Module: "local/l", Producer: "local/p", Run: "slot", Cells: []string{"p.soc"}, Depth: depth}
		if err := e.host.RunTrigger(context.Background(), trig, id); err != nil {
			t.Fatal(err)
		}
	}
	run(3, "01EV3")
	got := e.triggers(t)
	if len(got) != 1 || got[0].Module != "local/m" || got[0].Producer != "local/l" || got[0].Run != "01EV3" || got[0].Depth != 4 {
		t.Fatalf("triggers %+v", got)
	}
	run(4, "01EV4")
	run(4, "01EV5")
	if got := e.triggers(t); len(got) != 1 {
		t.Fatalf("a chain past depth 4 went on: %+v", got)
	}
	if n := strings.Count(e.logText(), "deeper than 4"); n != 1 {
		t.Fatalf("the stopped chain was told %d times:\n%s", n, e.logText())
	}
	if v := rec.Value(t, "ganglion.modhost.triggers", map[string]string{"module": "local/l", "outcome": "stopped"}); v != 2 {
		t.Errorf("stopped = %v, want 2", v)
	}
}

// A failing trigger run is unattended, like cron: it escalates its kind
// once and counts towards the crash loop (R-RUN-12, R-RUN-15).
func TestAFailingTriggerRunEscalatesAndCountsLikeCron(t *testing.T) {
	e := newEnv(t, active(t, "local/l", listenerManifest("p.soc")))
	e.start(t)
	e.state.Put("l", "op_cell", cronFails)
	for i := range 5 {
		trig := event.ModuleTrigger{Module: "local/l", Producer: "local/p", Run: "slot", Cells: []string{"p.soc"}, Depth: 1}
		if err := e.host.RunTrigger(context.Background(), trig, "01EV"+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Minute)
	}
	var kinds []string
	for _, m := range e.moduleEvents(t) {
		kinds = append(kinds, m.Kind)
	}
	if strings.Join(kinds, ",") != "auth_failed,module.crashloop" {
		t.Fatalf("escalations %v", kinds)
	}
	if !slices.Contains(e.conf.offs, "local/l:true:crashloop") {
		t.Fatalf("offs %v", e.conf.offs)
	}
	if n := len(e.store.failures()); n != 5 {
		t.Fatalf("%d failures counted", n)
	}
}
