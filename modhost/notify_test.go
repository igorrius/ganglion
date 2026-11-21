package modhost_test

import (
	"context"
	"encoding/json"
	"github.com/igorrius/ganglion/settings"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
)

// notifyManifest is a module that may write to its owner 24 times a day:
// its cron run and its tool send each call notify once.
const notifyManifest = `name: probe
version: 1
triggers:
  - cron: "*/30 * * * *"
  - tool: send
    about: Sends a message.
    output: {err: string}
capabilities:
  state: true
  notify: {per_day: 24}
config:
  op_cron: {type: string, default: '{"op":"notify","value":"battery *30* %"}'}
  op_tool_send: {type: string, default: '{"op":"notify","value":"hi"}'}
  lifecycle: {type: string, default: ""}
limits:
  timeout: 5s
`

// notified is a module.notify event and its payload.
type notified struct {
	ev event.Event
	p  event.ModuleNotify
}

// notifies are the module.notify events in the inbox, each checked for
// the node's source and the owner's private chat.
func (e *env) notifies(t *testing.T) []notified {
	t.Helper()
	var out []notified
	for _, ev := range e.inbox.taken() {
		if ev.Kind != event.KindModuleNotify {
			continue
		}
		var p event.ModuleNotify
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if ev.Source != event.SourceModule || ev.Conv != "telegram:42" || p.Principal != owner || p.Transport != "telegram" {
			t.Fatalf("event %+v, payload %+v", ev, p)
		}
		out = append(out, notified{ev: ev, p: p})
	}
	return out
}

// send calls the module's send tool as call n of turn 01TURN and returns
// the notify error it answered ("" when sent).
func (e *env) send(t *testing.T, n int) string {
	t.Helper()
	res, err := e.tool("n.send").Run(context.Background(), tool.Call{Name: "n.send", Args: json.RawMessage(`{}`), Admin: true,
		Principal: owner, TurnID: "01TURN", N: n})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Err string `json:"err"`
	}
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("answer %q: %v", res.Text, err)
	}
	return out.Err
}

// R-RUN-16: a cron run's message is one event in the owner's private chat,
// keyed by the run's slot and the call's number, carrying the module's text
// as it is (the agent escapes it); the same slot run again puts nothing new.
func TestACronRunNotifiesOnceForItsSlot(t *testing.T) {
	e := newEnv(t, active(t, "local/n", notifyManifest))
	e.start(t)
	e.runCron(t, "local/n", "cron:module:local/n:0:1")
	e.runCron(t, "local/n", "cron:module:local/n:0:1") // the event delivered again
	got := e.notifies(t)
	if len(got) != 1 {
		t.Fatalf("events %+v", got)
	}
	want := event.ModuleNotify{Module: "local/n", Version: "1", Text: "battery *30* %", PerDay: 24, At: t0.UnixMilli(), Principal: owner, Transport: "telegram", Run: "cron:module:local/n:0:1"}
	if got[0].ev.IdemKey != "notify:local/n:cron:module:local/n:0:1:1" || got[0].p != want {
		t.Fatalf("event %+v, payload %+v", got[0].ev, got[0].p)
	}
	e.runCron(t, "local/n", "cron:module:local/n:0:2")
	if got := e.notifies(t); len(got) != 2 || got[1].ev.IdemKey != "notify:local/n:cron:module:local/n:0:2:1" {
		t.Fatalf("the next slot: %+v", got)
	}
}

// Over per_day the module gets "limit" and nothing is sent; the owner gets
// one node line that day, and a new day in the node's zone starts afresh.
// Each answer is counted by its outcome.
func TestOverTheDailyLimitTheOwnerGetsOneLine(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/n", notifyManifest))
	e.start(t)
	for n := 1; n <= 24; n++ {
		if got := e.send(t, n); got != "" {
			t.Fatalf("message %d: %q", n, got)
		}
	}
	if got := e.send(t, 25); got != "limit" {
		t.Fatalf("the 25th: %q, want limit", got)
	}
	got := e.notifies(t)
	if len(got) != 25 {
		t.Fatalf("%d events, want 24 and the limit line", len(got))
	}
	line := got[24]
	want := event.ModuleNotify{Module: "local/n", Version: "1", Node: event.NotifyLimitLine, PerDay: 24, At: t0.UnixMilli(), Principal: owner, Transport: "telegram"}
	if line.ev.IdemKey != "notify:local/n:limit:2026-11-09" || line.p != want {
		t.Fatalf("limit line %+v, payload %+v", line.ev, line.p)
	}
	if got := e.send(t, 26); got != "limit" {
		t.Fatalf("the 26th: %q", got)
	}
	if got := e.notifies(t); len(got) != 25 {
		t.Fatalf("the 26th put an event: %d", len(got))
	}
	e.now = time.Date(2026, 11, 9, 22, 30, 0, 0, time.UTC) // 00:30 in Kyiv: a new day there, not in UTC
	if got := e.send(t, 27); got != "" {
		t.Fatalf("a new day: %q", got)
	}
	for outcome, want := range map[string]float64{"sent": 25, "limit": 2} {
		if v := rec.Value(t, "ganglion.modhost.notifies", map[string]string{"module": "local/n", "outcome": outcome}); v != want {
			t.Errorf("notifies %s = %v, want %v", outcome, v, want)
		}
	}
}

// init and health runs have no run identity: notify answers "not here" and
// puts nothing, so an install's health check never writes to the owner.
func TestInitAndHealthCannotNotify(t *testing.T) {
	e := newEnv(t, active(t, "local/n", strings.Replace(notifyManifest, `lifecycle: {type: string, default: ""}`,
		`lifecycle: {type: string, default: "notify"}`, 1)))
	e.start(t)
	if e.tool("n.send") == nil {
		t.Fatalf("the module did not start: its init's notify was not refused\n%s", e.logText())
	}
	card, err := e.host.Card(context.Background(), "n")
	if err != nil || card.Health != modstore.HealthOK {
		t.Fatalf("health %q, %v: its notify was not refused", card.Health, err)
	}
	if got := e.inbox.taken(); len(got) != 0 {
		t.Fatalf("events %+v", got)
	}
}

// A chain stopped past depth 4 tells the owner once per build, as a node
// line in their private chat.
func TestAStoppedChainTellsTheOwnerOnce(t *testing.T) {
	l := active(t, "local/l", listenerManifest("p.soc"))
	e := newEnv(t, l, active(t, "local/m", listenerManifest("l.x")))
	e.start(t)
	e.state.Put("l", "op_cell", `{"op":"cell_report","name":"x","json":1}`)
	for i := range 2 {
		trig := event.ModuleTrigger{Module: "local/l", Producer: "local/p", Run: "slot", Cells: []string{"p.soc"}, Depth: 4}
		if err := e.host.RunTrigger(context.Background(), trig, "01EV"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	got := e.notifies(t)
	want := event.ModuleNotify{Module: "local/l", Version: "1", Node: event.NotifyChainLine, At: t0.UnixMilli(), Principal: owner, Transport: "telegram"}
	if len(got) != 1 || got[0].ev.IdemKey != "notify:local/l:chain:"+l.SHA256 || got[0].p != want {
		t.Fatalf("events %+v", got)
	}
}

// A build's check before its install is no run either: its health's notify
// answers "not here" and nothing reaches the owner.
func TestABuildsCheckCannotNotify(t *testing.T) {
	e := newEnv(t)
	b := build(t, strings.Replace(notifyManifest, `lifecycle: {type: string, default: ""}`, `lifecycle: {type: string, default: "notify"}`, 1))
	if err := e.host.Check(context.Background(), b, nil); err != nil {
		t.Fatalf("check: %v\n%s", err, e.logText())
	}
	if got := e.inbox.taken(); len(got) != 0 {
		t.Fatalf("events %+v", got)
	}
}

// copies are the module.notify events in the inbox as "conv to key".
func (e *env) copies(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, ev := range e.inbox.taken() {
		if ev.Kind != event.KindModuleNotify {
			continue
		}
		var p event.ModuleNotify
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if ev.Source != event.SourceModule || p.Principal != owner || p.Transport != "telegram" {
			t.Fatalf("event %+v, payload %+v", ev, p)
		}
		out = append(out, ev.Conv+" to="+p.To+" key="+ev.IdemKey)
	}
	return out
}

// R-RUN-18: a module's message goes to every destination its line names,
// the owner's copy under the message's own key; the day counts one
// message; a run delivered again puts nothing twice; the module does not
// know the list.
func TestAMessageGoesToEveryDestinationOfTheLine(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/n", notifyManifest))
	if err := e.conf.SetModule(settings.Module{Name: "local/n", NotifyTo: []string{"family-k7", "owner"}}); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	e.runCron(t, "local/n", "cron:module:local/n:0:1")
	if got := rec.Value(t, "ganglion.modhost.notifies", map[string]string{"module": "local/n", "outcome": "sent"}); got != 1 {
		t.Errorf("sent = %v: a call is counted once, whatever its destinations", got)
	}
	e.runCron(t, "local/n", "cron:module:local/n:0:1") // the event delivered again
	const key = "notify:local/n:cron:module:local/n:0:1:1"
	want := []string{"telegram:-1001/45 to=family-k7 key=" + key + ":to:family-k7", "telegram:42 to= key=" + key}
	if got := e.copies(t); !slices.Equal(got, want) {
		t.Fatalf("copies %v, want %v", got, want)
	}
}

// A name that resolves to nothing, or cannot be resolved now, sends
// nothing there; the other copies go, and the module is told the message
// was sent. The names are resolved when the message is delivered, after
// the module was told, so with no copy at all it is still told "sent" and
// the node counts the copies it could not send.
func TestADestinationThatDoesNotResolveIsSkipped(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/n", notifyManifest))
	if err := e.conf.SetModule(settings.Module{Name: "local/n", NotifyTo: []string{"gone", "broken", "family-k7"}}); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	if got := e.send(t, 1); got != "" {
		t.Fatalf("the module was told %q, want sent", got)
	}
	if got := e.copies(t); len(got) != 1 || !strings.HasPrefix(got[0], "telegram:-1001/45 to=family-k7 ") {
		t.Fatalf("copies %v", got)
	}
	if got := rec.Value(t, "ganglion.modhost.notifies", map[string]string{"module": "local/n", "outcome": "no_destination"}); got != 2 {
		t.Errorf("no_destination = %v, want 2", got)
	}
	if err := e.conf.SetModule(settings.Module{Name: "local/n", NotifyTo: []string{"gone"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.host.Reload(context.Background(), "local/n"); err != nil {
		t.Fatal(err)
	}
	if got := e.send(t, 2); got != "" {
		t.Fatalf("the module was told %q, want sent: the names are resolved at delivery", got)
	}
	if got := e.copies(t); len(got) != 1 {
		t.Fatalf("copies %v: a message with no name that resolves put one", got)
	}
	if got := rec.Value(t, "ganglion.modhost.notifies", map[string]string{"module": "local/n", "outcome": "no_destination"}); got != 3 {
		t.Errorf("no_destination = %v, want 3", got)
	}
}

// Changing the line and reloading changes where the next message goes;
// the node's own lines about a module go to the owner only.
func TestTheLineIsReadWhenTheModuleLoads(t *testing.T) {
	e := newEnv(t, active(t, "local/n", notifyManifest))
	e.start(t)
	e.send(t, 1)
	if err := e.conf.SetModule(settings.Module{Name: "local/n", NotifyTo: []string{"family-k7"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.host.Reload(context.Background(), "local/n"); err != nil {
		t.Fatal(err)
	}
	e.send(t, 2)
	got := e.copies(t)
	if len(got) != 2 || !strings.HasPrefix(got[0], "telegram:42 to= ") || !strings.HasPrefix(got[1], "telegram:-1001/45 to=family-k7 ") {
		t.Fatalf("copies %v", got)
	}
	// Past the daily limit the limit line goes to the owner, not to the topic.
	for n := 3; n <= 26; n++ {
		e.send(t, n)
	}
	var lines []string
	for _, c := range e.copies(t) {
		if strings.Contains(c, ":limit:") {
			lines = append(lines, c)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "telegram:42 to= ") {
		t.Fatalf("limit lines %v", lines)
	}
}
