package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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

// Calls travel over links (R-BUS-6): a caller's call host function sends a
// request to the endpoint its link names, and the serving module runs for
// it. These tests run the host over a real embedded bus.

const (
	// callerManifest calls the port c: a number in, a string out. Its time
	// limit is 3 s, so a call waits for 2.
	callerManifest = probeManifest + "ports:\n  calls: {c: {in: number, out: string}}\n"
	callOp         = `{"op":"call","name":"c","json":1.5}`
)

var (
	// serverManifest serves the port s, with the shapes of c; its run answers
	// "yes" unless its state says otherwise (op_call_s).
	serverManifest = strings.Replace(probeManifest, "config:\n",
		"config:\n  op_call_s: {type: string, default: '{\"op\":\"answer\",\"json\":\"yes\"}'}\n", 1) + "ports:\n  serves: {s: {in: number, out: string}}\n"

	linkCS  = settings.Link{From: "a.c", To: "b.s"}
	linkCS2 = settings.Link{From: "a.c", To: "c.s"}
)

// callEnv is an env whose host serves and calls over a real bus, on the real
// clock: a deadline a call carries is a real one.
type callEnv struct {
	*env
	bus *natsbus.Bus
	rec *telemetrytest.Recorder
}

// newCallEnv installs "a", which calls, and the servers by short name; the
// links are ls. mutate may change the caller's manifest.
func newCallEnv(t *testing.T, caller string, servers []string, ls ...settings.Link) *callEnv {
	t.Helper()
	rec := telemetrytest.Install(t)
	b, err := natsbus.Open(context.Background(), natsbus.DefaultConfig(t.TempDir()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	caller = strings.Replace(caller, "limits:\n  timeout: 5s\n", "limits:\n  timeout: 3s\n", 1)
	actives := []modstore.Active{active(t, "local/a", caller)}
	for _, s := range servers {
		actives = append(actives, active(t, "local/"+s, serverManifest))
	}
	e := newEnv(t, actives...)
	e.conf.links = ls
	e.calls = b
	e.real = true
	e.host = e.newHost()
	return &callEnv{env: e, bus: b, rec: rec}
}

// callResult is what the caller's last call gave it, as the probe noted.
type callResult struct {
	Err    string          `json:"err"`
	Answer json.RawMessage `json:"answer"`
}

// result is the caller's last call result; it fails when there is none.
func (ce *callEnv) result(t *testing.T) callResult {
	t.Helper()
	raw, ok := ce.state.Get("a", "call_result")
	if !ok {
		t.Fatal("the caller noted no call result")
	}
	var r callResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return r
}

// ask runs the caller's cron once with op, as the run of slot, and returns
// what its call got.
func (ce *callEnv) ask(t *testing.T, op, slot string) callResult {
	t.Helper()
	ce.state.Put("a", "call_result", "")
	ce.state.Put("a", "op_cron", op)
	ce.runCron(t, "local/a", slot)
	return ce.result(t)
}

// callRuns are the runs of module that a call started.
func (ce *callEnv) callRuns(module string) []modstore.Run {
	runs, _ := ce.store.Runs(context.Background(), module, 20)
	return slices.DeleteFunc(runs, func(r modstore.Run) bool { return r.Trigger != "call" })
}

func (ce *callEnv) outcome(t *testing.T, module, outcome string) float64 {
	t.Helper()
	return ce.rec.Value(t, "ganglion.modhost.calls", map[string]string{"module": module, "outcome": outcome})
}

func (ce *callEnv) served(t *testing.T, module, outcome string) float64 {
	t.Helper()
	return ce.rec.Value(t, "ganglion.modhost.served", map[string]string{"module": module, "port": "s", "outcome": outcome})
}

// request sends a raw request to b's port s, as a caller's host would, with
// headers; its error is a *bus.CallError for an error answer.
func (ce *callEnv) request(t *testing.T, headers map[string]string, payload string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return ce.bus.Request(ctx, "g.rpc.core.b.s", headers, []byte(payload))
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *bus.CallError
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("error %v, want an answer with code %q", err, code)
	}
	if strings.Contains(ce.Description, "hunter") || strings.Contains(ce.Description, hostile) {
		t.Fatalf("the description holds module text: %q", ce.Description)
	}
}

// A call is answered across two modules: the server runs with the request
// and the calling port, one level deeper than the caller's run, the journal
// names the caller and its run, and both sides count it.
func TestACallIsAnsweredAcrossTwoModules(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	if got := ce.ask(t, callOp, "slot-1"); got.Err != "" || string(got.Answer) != `"yes"` {
		t.Fatalf("the call got %+v, want the answer yes", got)
	}
	rows := ce.callRuns("local/b")
	if len(rows) != 1 {
		t.Fatalf("b ran %d times for the call, want 1", len(rows))
	}
	if r := rows[0]; r.Depth != 1 || r.Producer != "local/a" || r.ProducerRun != "slot-1" || r.Result != "ok" || !strings.HasPrefix(r.ID, "call:") {
		t.Errorf("b's row: %+v", r)
	}
	if got, _ := ce.state.Get("b", "last_call"); got != `{"from":"a.c","request":1.5}` {
		t.Errorf("b was given %s", got)
	}
	runs, _ := ce.store.Runs(context.Background(), "local/a", 1)
	if len(runs) != 1 || runs[0].Calls["call"] != 1 {
		t.Errorf("a's row: %+v, want one call", runs)
	}
	if got := ce.outcome(t, "local/a", "answered"); got != 1 {
		t.Errorf("calls answered = %v, want 1", got)
	}
	if got := ce.served(t, "local/b", "ok"); got != 1 {
		t.Errorf("served ok = %v, want 1", got)
	}
}

// A calling port with no link, one whose link is broken and one the link
// leaves to a port that is not served are all denied; nothing is sent.
func TestACallWithoutAWorkingLinkIsDenied(t *testing.T) {
	for name, ls := range map[string][]settings.Link{
		"no link":        nil,
		"a missing port": {{From: "a.c", To: "b.nope"}},
		"another kind":   {{From: "a.c", To: "b.y"}},
		"a missing end":  {{From: "a.c", To: "q.s"}},
	} {
		t.Run(name, func(t *testing.T) {
			ce := newCallEnv(t, callerManifest, []string{"b"}, ls...)
			ce.start(t)
			if got := ce.ask(t, callOp, "slot-1"); got.Err != "denied" {
				t.Fatalf("the call got %+v, want denied", got)
			}
			if n := len(ce.callRuns("local/b")); n != 0 {
				t.Fatalf("b ran %d times", n)
			}
			if got := ce.outcome(t, "local/a", "denied"); got != 1 {
				t.Errorf("calls denied = %v, want 1", got)
			}
		})
	}
}

// A calling port has one link: when the file holds two, the first works and
// the second is broken and carries nothing.
func TestOnlyTheFirstLinkOfACallingPortWorks(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b", "c"}, linkCS, linkCS2)
	ce.start(t)
	if got := ce.ask(t, callOp, "slot-1"); got.Err != "" {
		t.Fatalf("the call got %+v", got)
	}
	if len(ce.callRuns("local/b")) != 1 || len(ce.callRuns("local/c")) != 0 {
		t.Fatalf("b ran %d times and c %d, want 1 and 0", len(ce.callRuns("local/b")), len(ce.callRuns("local/c")))
	}
}

// A server that is off is nobody: the call fails at once with unavailable,
// and works again when the module is on.
func TestACallToAServerThatIsOffIsUnavailable(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	ctx := context.Background()
	if err := ce.host.SetOff(ctx, "b", true); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if got := ce.ask(t, callOp, "slot-1"); got.Err != "unavailable" {
		t.Fatalf("the call got %+v, want unavailable", got)
	}
	if time.Since(started) > 2*time.Second {
		t.Error("an absent server took the time of a wait")
	}
	if got := ce.outcome(t, "local/a", "unavailable"); got != 1 {
		t.Errorf("calls unavailable = %v, want 1", got)
	}
	if err := ce.host.SetOff(ctx, "b", false); err != nil {
		t.Fatal(err)
	}
	if got := ce.ask(t, callOp, "slot-2"); got.Err != "" || string(got.Answer) != `"yes"` {
		t.Fatalf("after on the call got %+v, want the answer", got)
	}
}

// An answer outside the port's out shape is failed for the caller and a
// module.format failure of the serving run, in the node's words.
func TestAnAnswerOutsideTheOutShapeIsFailed(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	ce.state.Put("b", "op_call_s", `{"op":"answer","json":12}`)
	if got := ce.ask(t, callOp, "slot-1"); got.Err != "failed" {
		t.Fatalf("the call got %+v, want failed", got)
	}
	rows := ce.callRuns("local/b")
	if len(rows) != 1 || rows[0].Kind != "module.format" {
		t.Fatalf("b's rows: %+v, want one module.format", rows)
	}
	if got := ce.served(t, "local/b", "failed"); got != 1 {
		t.Errorf("served failed = %v, want 1", got)
	}
}

// A request that breaks the in shape is not sent, and the caller's run
// fails as module.format.
func TestARequestOutsideTheInShapeIsNotSent(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	ce.state.Put("a", "op_cron", `{"op":"call","name":"c","json":"text"}`)
	ce.runCron(t, "local/a", "slot-1")
	runs, _ := ce.store.Runs(context.Background(), "local/a", 1)
	if len(runs) != 1 || runs[0].Kind != "module.format" {
		t.Fatalf("a's run: %+v, want module.format", runs)
	}
	if n := len(ce.callRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times", n)
	}
	if got := ce.outcome(t, "local/a", "format"); got != 1 {
		t.Errorf("calls format = %v, want 1", got)
	}
}

// The serving side checks what comes to it, whoever sent it: a request
// outside the port's shape, one of a chain at the depth limit and one whose
// caller gave up start no run, and each gets an error answer in the node's
// words.
func TestTheServingSideRefusesWhatItShouldNotRun(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	now := func(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).UnixMilli(), 10) }

	_, err := ce.request(t, map[string]string{bus.HeaderDepth: "0", bus.HeaderDeadline: now(time.Minute)}, `"hunter-two"`)
	wantCode(t, err, bus.CodeFormat)
	_, err = ce.request(t, map[string]string{bus.HeaderDepth: "4", bus.HeaderDeadline: now(time.Minute)}, `1`)
	wantCode(t, err, bus.CodeDeep)
	_, err = ce.request(t, map[string]string{bus.HeaderDepth: "4", bus.HeaderDeadline: now(time.Minute)}, `2`)
	wantCode(t, err, bus.CodeDeep)
	_, err = ce.request(t, map[string]string{bus.HeaderDepth: "0", bus.HeaderDeadline: now(-time.Second)}, `1`)
	wantCode(t, err, bus.CodeLate)

	if n := len(ce.callRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times", n)
	}
	for outcome, want := range map[string]float64{"bad": 1, "deep": 2, "late": 1} {
		if got := ce.served(t, "local/b", outcome); got != want {
			t.Errorf("served %s = %v, want %v", outcome, got, want)
		}
	}
	if strings.Contains(ce.logText(), "hunter-two") {
		t.Error("the log holds the request's value")
	}
	if n := strings.Count(ce.logText(), "its calls start no module"); n != 1 {
		t.Errorf("the chain was logged %d times, want once per build", n)
	}
	chain := 0
	for _, ev := range ce.inbox.taken() {
		if strings.Contains(ev.IdemKey, ":chain:") {
			chain++
		}
	}
	if chain != 1 {
		t.Errorf("the owner got %d chain lines, want 1", chain)
	}
}

// A serving run that fails is counted toward the crash loop but not
// escalated: the caller sees the failure, in the node's words; the fifth
// failure turns the module off and escalates that.
func TestAFailedServingRunIsCountedAndNotEscalated(t *testing.T) {
	ce := newCallEnv(t, callerManifest, []string{"b"}, linkCS)
	ce.start(t)
	ce.state.Put("b", "op_call_s", cronFails) // fails as auth_failed, which the module escalates when unattended
	for i := range 4 {
		got := ce.ask(t, callOp, "slot-"+strconv.Itoa(i))
		if got.Err != "failed" {
			t.Fatalf("call %d got %+v, want failed", i, got)
		}
	}
	if evs := ce.moduleEvents(t); len(evs) != 0 {
		t.Fatalf("four failed calls escalated: %+v", evs)
	}
	if rows := ce.callRuns("local/b"); len(rows) != 4 || rows[0].Result != "failed" {
		t.Fatalf("b's rows: %+v", rows)
	}
	_, err := ce.request(t, map[string]string{bus.HeaderDepth: "0"}, `1`)
	wantCode(t, err, bus.CodeFailed)
	if strings.Join(ce.conf.offs, ",") != "local/b:true:crashloop" {
		t.Fatalf("settings writes %v, want the crash loop of b", ce.conf.offs)
	}
	for _, m := range ce.moduleEvents(t) {
		if m.Kind != "module.crashloop" {
			t.Errorf("escalated %q", m.Kind)
		}
	}
	if got := ce.ask(t, callOp, "slot-9"); got.Err != "unavailable" {
		t.Fatalf("a call to the module that is off got %+v, want unavailable", got)
	}
}

// A reload that waits for the host's lock holds the serving side back
// behind it, because the caller's run holds the lock for reading while it
// waits for the answer. The wait is bounded: the caller gives up, its run
// ends, the reload goes through, and the serving side, which sees the
// deadline the request carried, runs nothing for a caller that has gone.
func TestAReloadDuringACallIsBounded(t *testing.T) {
	gated := strings.Replace(callerManifest, "capabilities:\n", "capabilities:\n  http.fetch: [127.0.0.1]\n", 1)
	ce := newCallEnv(t, gated, []string{"b"}, linkCS)
	ce.start(t)
	arrived, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(arrived) })
		<-gate
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ce.state.Put("a", "call_result", "")
	ce.state.Put("a", "op_cron", fmt.Sprintf(`{"op":"fetch_call","url":%q,"name":"c","json":1}`, srv.URL))
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		ce.runCron(t, "local/a", "slot-1")
	}()
	<-arrived // the caller's run holds the lock, for reading
	reloaded := make(chan error, 1)
	go func() { reloaded <- ce.host.Reload(context.Background(), "local/b") }()
	waitFor(t, "the reload to wait for the lock", ce.host.WriterWaiting)
	started := time.Now()
	close(gate) // the caller goes on and calls: its request queues behind the reload

	select {
	case err := <-reloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reload did not go through")
	}
	<-ran
	if waited := time.Since(started); waited < time.Second || waited > 10*time.Second {
		t.Errorf("the caller waited %v, want about its window of 2 s", waited)
	}
	if got := ce.result(t); got.Err != "failed" {
		t.Fatalf("the call got %+v, want failed: the answer did not come in time", got)
	}
	waitFor(t, "the serving side to see the deadline", func() bool { return ce.served(t, "local/b", "late") == 1 })
	if n := len(ce.callRuns("local/b")); n != 0 {
		t.Fatalf("b ran %d times for a caller that had gone", n)
	}
	if got := ce.ask(t, callOp, "slot-2"); got.Err != "" { // the reloaded module serves again
		t.Fatalf("after the reload the call got %+v", got)
	}
}

// fakeCalls records the services a host registers and the requests it sends.
type fakeCalls struct {
	mu       sync.Mutex
	services []*fakeService
}

type fakeService struct {
	name, version string
	endpoints     []bus.Endpoint
	stopped       bool
}

func (f *fakeCalls) Serve(name, version string, endpoints []bus.Endpoint) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &fakeService{name: name, version: version, endpoints: endpoints}
	f.services = append(f.services, s)
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		s.stopped = true
		return nil
	}, nil
}

func (f *fakeCalls) Request(context.Context, string, map[string]string, []byte) ([]byte, error) {
	return nil, bus.ErrNoResponder
}

// live are the services registered and not stopped, as name@version.
func (f *fakeCalls) live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.services {
		if !s.stopped {
			out = append(out, s.name+"@"+s.version)
		}
	}
	return out
}

// Every running module with served ports is a micro service named after it,
// at its manifest's version, with an endpoint per port on its subject and
// the shapes in the metadata; a module with none registers nothing. The
// service follows the module: replaced by a reload, gone when it is off or
// the host shuts down.
func TestAServiceFollowsItsModule(t *testing.T) {
	fc := &fakeCalls{}
	e := newEnv(t, active(t, "local/a", callerManifest), active(t, "local/b", serverManifest))
	e.calls = fc
	e.host = e.newHost()
	e.start(t)
	ctx := context.Background()
	if got := fc.live(); !slices.Equal(got, []string{"b@3.0.0"}) {
		t.Fatalf("services %v, want b@3.0.0 only", got)
	}
	eps := fc.services[0].endpoints
	if len(eps) != 1 || eps[0].Name != "s" || eps[0].Subject != "g.rpc.core.b.s" || eps[0].Metadata["in"] != "number" || eps[0].Metadata["out"] != "string" {
		t.Fatalf("endpoints %+v", eps)
	}
	if err := e.host.Reload(ctx, "local/b"); err != nil {
		t.Fatal(err)
	}
	if got := fc.live(); !slices.Equal(got, []string{"b@3.0.0"}) || len(fc.services) != 2 {
		t.Fatalf("after reload: live %v of %d registered, want the new one only", got, len(fc.services))
	}
	if err := e.host.SetOff(ctx, "b", true); err != nil {
		t.Fatal(err)
	}
	if got := fc.live(); len(got) != 0 {
		t.Fatalf("after off: %v", got)
	}
	if err := e.host.SetOff(ctx, "b", false); err != nil {
		t.Fatal(err)
	}
	if got := fc.live(); len(got) != 1 {
		t.Fatalf("after on: %v", got)
	}
	if err := e.host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fc.live(); len(got) != 0 {
		t.Fatalf("after shutdown: %v", got)
	}
}
