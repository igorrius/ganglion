package modhost_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// Events travel over links (R-BUS-6): a producer's emit is stored on the
// EVENTS stream, and one durable consumer per link runs the receiver. These
// tests run the host over a real embedded bus.

// producerEvents emits on x; receivers accept y (the same shape). The
// receivers' event run is scripted through the state key op_event_y.
const (
	eventProducer = probeManifest + "ports:\n  emits: {x: number}\n"
	emitX         = `{"op":"emit","name":"x","json":1.5}`
)

// eventReceiver echoes when an event reaches y, unless its state says
// otherwise.
var eventReceiver = strings.Replace(probeManifest, "config:\n", "config:\n  op_event_y: {type: string, default: '{\"op\":\"echo\"}'}\n", 1) +
	"ports:\n  accepts: {y: number}\n"

// eventEnv is an env whose host publishes to, and consumes from, a real
// bus.
type eventEnv struct {
	*env
	bus *natsbus.Bus
	rec *telemetrytest.Recorder
}

// newEventEnv installs the modules by short name: "a" produces, the others
// receive; the links are ls.
func newEventEnv(t *testing.T, receivers []string, ls ...settings.Link) *eventEnv {
	t.Helper()
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	b, err := natsbus.Open(ctx, natsbus.DefaultConfig(t.TempDir()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	actives := []modstore.Active{active(t, "local/a", eventProducer)}
	for _, r := range receivers {
		actives = append(actives, active(t, "local/"+r, eventReceiver))
	}
	e := newEnv(t, actives...)
	e.conf.links = ls
	e.pub, e.consumers = b, b
	e.host = e.newHost()
	return &eventEnv{env: e, bus: b, rec: rec}
}

// waitFor polls cond until it holds; a condition that never does fails the
// test with what.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// consumers are the link consumers on the bus.
func (ee *eventEnv) consumerNames(t *testing.T) []string {
	t.Helper()
	names, err := ee.bus.ConsumerNames(context.Background(), bus.StreamEvents, "link_")
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// waitConsumers waits until n link consumers exist on the bus, and returns
// their names.
func (ee *eventEnv) waitConsumers(t *testing.T, n int) []string {
	t.Helper()
	var names []string
	waitFor(t, "the link consumers", func() bool {
		names = ee.consumerNames(t)
		return len(names) == n
	})
	return names
}

// runsOf are the runs of module in the journal that an event started.
func (ee *eventEnv) eventRuns(module string) []modstore.Run {
	runs, _ := ee.store.Runs(context.Background(), module, 20)
	return slices.DeleteFunc(runs, func(r modstore.Run) bool { return r.Trigger != "event" })
}

// lastEvent is what the module's last event run was given, noted in its
// state by the probe.
func (ee *eventEnv) lastEvent(short string) (string, bool) { return ee.state.Get(short, "last_event") }

// pendingOf is how many events the link's consumer has not acked.
func (ee *eventEnv) pendingOf(t *testing.T, name string) int {
	t.Helper()
	n, err := ee.bus.ConsumerPending(context.Background(), bus.StreamEvents, name)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// publishRaw puts an event on a's x port as if a run sent it, with a depth.
func (ee *eventEnv) publishRaw(t *testing.T, id, depth, payload string) {
	t.Helper()
	err := ee.bus.Publish(context.Background(), "g.ev.core.a.x", id,
		map[string]string{bus.HeaderFrom: "core/local/a", bus.HeaderRun: "run-x", bus.HeaderDepth: depth}, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
}

func (ee *eventEnv) eventsOnStream(t *testing.T) uint64 {
	t.Helper()
	s, err := ee.bus.JetStream().Stream(context.Background(), bus.StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

var (
	linkAB = settings.Link{From: "a.x", To: "b.y"}
	linkAC = settings.Link{From: "a.x", To: "c.y"}
)

// One event reaches two modules by two links: each receiver runs once with
// the event and where it came from, one level deeper than the producer's
// run, and the journal names the producer and its run.
func TestAnEventReachesTwoModulesByTwoLinks(t *testing.T) {
	ee := newEventEnv(t, []string{"b", "c"}, linkAB, linkAC)
	ee.start(t)
	ee.waitConsumers(t, 2)
	ee.state.Put("a", "op_cron", emitX)
	ee.runCron(t, "local/a", "slot-1")
	for _, m := range []string{"b", "c"} {
		waitFor(t, m+" to run", func() bool { return len(ee.eventRuns("local/"+m)) == 1 })
		got, _ := ee.lastEvent(m)
		if want := `{"from":"a.x","event":1.5}`; got != want {
			t.Errorf("%s was given %s, want %s", m, got, want)
		}
		r := ee.eventRuns("local/" + m)[0]
		if r.Depth != 1 || r.Producer != "local/a" || r.ProducerRun != "slot-1" || r.Result != "ok" || !strings.HasPrefix(r.ID, "event:link_") {
			t.Errorf("%s's row: %+v", m, r)
		}
	}
	if got := ee.eventsOnStream(t); got != 1 {
		t.Errorf("EVENTS holds %d messages, want 1 for two links", got)
	}
	if got := ee.rec.Value(t, "ganglion.modhost.events", map[string]string{"module": "local/a", "outcome": "sent"}); got != 1 {
		t.Errorf("events sent = %v, want 1", got)
	}
	for _, name := range ee.consumerNames(t) {
		if got := ee.rec.Value(t, "ganglion.modhost.event.deliveries", map[string]string{"consumer": name, "outcome": "ok"}); got != 1 {
			t.Errorf("deliveries ok of %s = %v, want 1", name, got)
		}
	}
}

// A run that fails is delivered again with the back-off, and the retry is
// its own row; the event is acked when a run succeeds.
func TestAFailedEventRunIsDeliveredAgain(t *testing.T) {
	ee := newEventEnv(t, []string{"b"}, linkAB)
	ee.start(t)
	names := ee.waitConsumers(t, 1)
	ee.state.Put("b", "op_event_y", cronFails)
	ee.state.Put("a", "op_cron", emitX)
	ee.runCron(t, "local/a", "slot-1")
	waitFor(t, "the first, failed run", func() bool { return len(ee.eventRuns("local/b")) == 1 })
	ee.state.Put("b", "op_event_y", "") // the receiver recovers: the next try runs the default operation
	waitFor(t, "the retry", func() bool {
		for _, r := range ee.eventRuns("local/b") {
			if r.Result == "ok" && strings.HasSuffix(r.ID, ":2") {
				return true
			}
		}
		return false
	})
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, names[0]) == 0 })
	if got := ee.rec.Value(t, "ganglion.modhost.event.deliveries", map[string]string{"consumer": names[0], "outcome": "failed"}); got != 1 {
		t.Errorf("deliveries failed = %v, want 1", got)
	}
}

// A module that is off keeps its events: they wait on the bus, and run
// when the module is on again. A module turned off does not lose its
// consumer on the server.
func TestAModuleThatIsOffKeepsItsEvents(t *testing.T) {
	ee := newEventEnv(t, []string{"b"}, linkAB)
	ee.start(t)
	names := ee.waitConsumers(t, 1)
	ctx := context.Background()
	if err := ee.host.SetOff(ctx, "b", true); err != nil {
		t.Fatal(err)
	}
	if got := ee.consumerNames(t); !slices.Equal(got, names) {
		t.Fatalf("consumers after off = %v, want %v kept", got, names)
	}
	ee.state.Put("a", "op_cron", emitX)
	ee.runCron(t, "local/a", "slot-1")
	waitFor(t, "the event to wait", func() bool { return ee.pendingOf(t, names[0]) == 1 })
	if n := len(ee.eventRuns("local/b")); n != 0 {
		t.Fatalf("a module that is off ran %d times", n)
	}
	if err := ee.host.SetOff(ctx, "b", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kept event to run", func() bool { return len(ee.eventRuns("local/b")) == 1 })
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, names[0]) == 0 })
}

// A link made after an event was sent does not replay it.
func TestANewLinkDoesNotReplayTheStream(t *testing.T) {
	ee := newEventEnv(t, []string{"b"})
	ee.start(t)
	ee.state.Put("a", "op_cron", emitX)
	ee.runCron(t, "local/a", "slot-1")
	ee.conf.links = []settings.Link{linkAB}
	ee.host.SyncLinks(context.Background())
	names := ee.waitConsumers(t, 1)
	ee.publishRaw(t, "after", "0", "2")
	waitFor(t, "the new event to run", func() bool { return len(ee.eventRuns("local/b")) == 1 })
	if got, _ := ee.lastEvent("b"); got != `{"from":"a.x","event":2}` {
		t.Fatalf("b was given %s, want only the event sent after the link", got)
	}
	waitFor(t, "the ack", func() bool { return ee.pendingOf(t, names[0]) == 0 })
	if n := len(ee.eventRuns("local/b")); n != 1 {
		t.Fatalf("b ran %d times, want 1", n)
	}
}

// An event of a chain at the depth limit is acked and runs nothing; the
// log and the owner's chain line say so once.
func TestAnEventAtTheDepthLimitRunsNothing(t *testing.T) {
	ee := newEventEnv(t, []string{"b"}, linkAB)
	ee.start(t)
	names := ee.waitConsumers(t, 1)
	ee.publishRaw(t, "deep-1", "4", "1")
	ee.publishRaw(t, "deep-2", "4", "2")
	waitFor(t, "both to be acked", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.event.deliveries", map[string]string{"consumer": names[0], "outcome": "deep"}) == 2 && ee.pendingOf(t, names[0]) == 0
	})
	if n := len(ee.eventRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times past the depth limit", n)
	}
	if n := strings.Count(ee.logText(), "its events start no module"); n != 1 {
		t.Errorf("the chain was logged %d times, want once per build", n)
	}
	chain := 0
	for _, ev := range ee.inbox.taken() {
		if strings.Contains(ev.IdemKey, ":chain:") {
			chain++
		}
	}
	if chain != 1 {
		t.Errorf("the owner got %d chain lines, want 1", chain)
	}
}

// An event that does not fit the receiver's port is acked, counted bad and
// logged without its value.
func TestABadEventIsAckedAndNeverRuns(t *testing.T) {
	ee := newEventEnv(t, []string{"b"}, linkAB)
	ee.start(t)
	names := ee.waitConsumers(t, 1)
	ee.publishRaw(t, "bad-1", "0", `"hunter-two"`)
	waitFor(t, "the bad event to be acked", func() bool {
		return ee.rec.Value(t, "ganglion.modhost.event.deliveries", map[string]string{"consumer": names[0], "outcome": "bad"}) == 1 && ee.pendingOf(t, names[0]) == 0
	})
	if n := len(ee.eventRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times for a payload that does not fit", n)
	}
	if strings.Contains(ee.logText(), "hunter-two") {
		t.Fatal("the log holds the payload's value")
	}
}

// A link that leaves the file loses its consumer on the server; the other
// links keep theirs.
func TestARemovedLinkLosesItsConsumer(t *testing.T) {
	ee := newEventEnv(t, []string{"b", "c"}, linkAB, linkAC)
	ee.start(t)
	before := ee.waitConsumers(t, 2)
	ee.conf.links = []settings.Link{linkAC}
	ee.host.SyncLinks(context.Background())
	after := ee.waitConsumers(t, 1)
	if !slices.Contains(before, after[0]) {
		t.Fatalf("consumers %v then %v", before, after)
	}
	ee.state.Put("a", "op_cron", emitX)
	ee.runCron(t, "local/a", "slot-1")
	waitFor(t, "c to run", func() bool { return len(ee.eventRuns("local/c")) == 1 })
	if n := len(ee.eventRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times over a removed link", n)
	}
}

// A payload that does not fit the emits port is not sent, and its run
// fails as module.format.
func TestAnEmitOutsideItsShapeIsNotSent(t *testing.T) {
	ee := newEventEnv(t, []string{"b"}, linkAB)
	ee.start(t)
	ee.waitConsumers(t, 1)
	ee.state.Put("a", "op_cron", `{"op":"emit","name":"x","json":"text"}`)
	ee.runCron(t, "local/a", "slot-1")
	if got := ee.eventsOnStream(t); got != 0 {
		t.Fatalf("EVENTS holds %d messages, want none", got)
	}
	runs, _ := ee.store.Runs(context.Background(), "local/a", 1)
	if len(runs) != 1 || runs[0].Kind != "module.format" {
		t.Fatalf("the run: %+v, want module.format", runs)
	}
	if got := ee.rec.Value(t, "ganglion.modhost.events", map[string]string{"module": "local/a", "outcome": "format"}); got != 1 {
		t.Errorf("events format = %v, want 1", got)
	}
}

// A notice that was sent is also an event on the module's notify output:
// a link from it carries {text, at} to a module that accepts that shape.
func TestANoticeIsAnEventOnTheNotifyOutput(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	b, err := natsbus.Open(ctx, natsbus.DefaultConfig(t.TempDir()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	receiver := strings.Replace(probeManifest, "config:\n", "config:\n  op_event_msg: {type: string, default: '{\"op\":\"echo\"}'}\n", 1) + "ports:\n  accepts: {msg: {text: string, at: string}}\n"
	e := newEnv(t, active(t, "local/n", notifyManifest), active(t, "local/r", receiver))
	e.conf.links = []settings.Link{{From: "n.notify", To: "r.msg"}}
	e.pub, e.consumers = b, b
	e.host = e.newHost()
	ee := &eventEnv{env: e, bus: b, rec: rec}
	ee.start(t)
	ee.waitConsumers(t, 1)
	ee.runCron(t, "local/n", "slot-1") // notifies "battery *30* %"
	waitFor(t, "r to run", func() bool { return len(ee.eventRuns("local/r")) == 1 })
	got, _ := ee.lastEvent("r")
	var in struct {
		From  string `json:"from"`
		Event struct {
			Text string `json:"text"`
			At   string `json:"at"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(got), &in); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if in.From != "n.notify" || in.Event.Text != "battery *30* %" || in.Event.At != t0.Format(time.RFC3339) {
		t.Fatalf("r was given %+v", in)
	}
}

// Reloads, link changes and events at once: the reconciles and the
// consumers share the host without a deadlock or a race, and Shutdown
// waits for every consumer's goroutine.
func TestReconcilesRunAlongsideReloadsAndEvents(t *testing.T) {
	ee := newEventEnv(t, []string{"b", "c"}, linkAB, linkAC)
	ee.start(t)
	ee.waitConsumers(t, 2)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for range 5 {
				if err := ee.host.Reload(ctx, "local/b"); err != nil {
					t.Error(err)
				}
				ee.host.SyncLinks(ctx)
			}
		})
	}
	wg.Go(func() {
		for i := range 5 {
			ee.publishRaw(t, "burst-"+string(rune('a'+i)), "0", "1")
		}
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the reconciles, reloads and events did not finish")
	}
	waitFor(t, "every event to reach c", func() bool { return len(ee.eventRuns("local/c")) == 5 })
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ee.host.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	ee.host.SyncLinks(ctx) // after Shutdown nothing starts again
	if n := len(ee.eventRuns("local/c")); n != 5 {
		t.Errorf("c ran %d times, want 5", n)
	}
}
