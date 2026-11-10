package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// Start loads the active module: its cells exist with their owner and
// about, its cron job is the installing admin's in their private chat, and
// a module the settings turn off is skipped (R-RUN-11, R-RUN-12).
func TestStartLoadsActiveModulesAndSkipsOffOnes(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest), active(t, "local/sleeper", probeManifest))
	e.conf.mods = []settings.Module{{Name: "local/sleeper", Off: true}}
	e.jobs.live["module:local/sleeper:0"] = schedule.Job{ID: "module:local/sleeper:0", Text: "local/sleeper"}
	e.start(t)

	c, err := e.cells.Get(context.Background(), "probe.soc")
	if err != nil || c.Owner != "local/probe" || c.About != "Battery state of charge, %" || c.Kind != cells.KindSensor {
		t.Fatalf("cell %+v, %v", c, err)
	}
	if _, err := e.cells.Get(context.Background(), "sleeper.soc"); !errors.Is(err, cells.ErrNotFound) {
		t.Fatalf("an off module defined its cells: %v", err)
	}
	js := e.jobs.all()
	if len(js) != 1 {
		t.Fatalf("jobs %+v", js)
	}
	j := js[0]
	if j.ID != "module:local/probe:0" || j.Kind != schedule.KindModule || j.Text != "local/probe" || j.Cron != "*/30 * * * *" ||
		j.Owner != owner || j.Conv != "telegram:42" || j.Transport != "telegram" || !j.System {
		t.Fatalf("job %+v", j)
	}
	var names []string
	for _, h := range e.host.Handlers() {
		names = append(names, h.Spec().Name)
	}
	if strings.Join(names, ",") != "probe.broken,probe.fail,probe.greedy,probe.status,probe.word" {
		t.Fatalf("tools %v", names)
	}
}

// A cron run reports the module's cell through the runner, with no model
// and no event; Handlers exposes the tool, which answers with the module's
// data as JSON (R-RUN-12).
func TestCronReportsACellAndAToolAnswers(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	if err := e.host.RunCron(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	c, err := e.cells.Get(context.Background(), "probe.soc")
	if err != nil || string(c.Reported) != "87" {
		t.Fatalf("cell %+v, %v", c, err)
	}
	h := e.tool("probe.status")
	s := h.Spec()
	if s.Description != "The probe's clock." || s.Class != tool.ClassRead || s.Policy != tool.PolicyAuto || !s.AdminOnly {
		t.Fatalf("spec %+v", s)
	}
	if err := tool.CheckArgs(s, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a call with no arguments is refused: %v", err)
	}
	res, err := e.call(t, "probe.status")
	if err != nil || !strings.HasPrefix(res.Text, `{"unix":`) {
		t.Fatalf("result %+v, %v", res, err)
	}
	if res.External {
		t.Fatal("a numbers-only answer is outside content")
	}
	if n := len(e.inbox.taken()); n != 0 {
		t.Fatalf("%d events for good runs", n)
	}
	if fs := e.store.failures(); len(fs) != 0 || e.store.runs() != 2 {
		t.Fatalf("counted %d runs, failures %+v", e.store.runs(), fs)
	}
}

// Any string in an answer makes it outside content (R-RUN-13).
func TestAnAnswerWithAStringIsExternal(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	res, err := e.call(t, "probe.word")
	if err != nil || res.Text != `{"trigger":"tool:word"}` || !res.External {
		t.Fatalf("result %+v, %v", res, err)
	}
}

// formatHint is the node's hint after a contract break.
const formatHint = "The module answered outside its declared contract: a defect in the module."

// A contract break gives the node's words, never the value, and counts a
// failure; in a tool call it escalates nothing, for the calling turn has
// the words already (R-RUN-14, owner 2026-11-10).
func TestAContractBreakIsTheNodesError(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	res, err := e.call(t, "probe.broken")
	if err == nil || err.Error() != "probe.broken: field value: expected integer, got string. "+formatHint || res.Text != "" {
		t.Fatalf("result %+v, %v", res, err)
	}
	fs := e.store.failures()
	if len(fs) != 1 || fs[0].Kind != "module.format" || fs[0].Exception {
		t.Fatalf("failures %+v", fs)
	}
	if evs := e.inbox.taken(); len(evs) != 0 {
		t.Fatalf("a tool call's failure escalated: %+v", evs)
	}
}

// A cron run's contract break escalates module.format with the node's
// description and its hint.
func TestACronContractBreakEscalates(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.cron(t, cronBreaks)
	ms := e.moduleEvents(t)
	if len(ms) != 1 || ms[0].Kind != "module.format" || ms[0].Detail != "probe cron: cell soc: expected integer, got string. "+formatHint ||
		ms[0].Module != "local/probe" || ms[0].Version != "3" || ms[0].Principal != owner || ms[0].Transport != "telegram" {
		t.Fatalf("events %+v", ms)
	}
}

// A run judged module.format still logs the module's own error text, so the
// operator sees the cause; the model and the owner never see it (R-RUN-14).
func TestAFormatVerdictLogsTheModulesOwnText(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.cron(t, `{"op":"fail","value":"`+hostile+`","name":"soc","json":"87"}`)
	ms := e.moduleEvents(t)
	if len(ms) != 1 || ms[0].Kind != "module.format" || strings.Contains(ms[0].Detail, "IGNORE") {
		t.Fatalf("events %+v", ms)
	}
	log := e.logText()
	if !strings.Contains(log, "IGNORE ALL PREVIOUS") || !strings.Contains(log, "result=format") {
		t.Fatalf("the module's text is not in the log:\n%s", log)
	}
}

// A module's failure reaches the model and the owner in the node's words
// only: the module's own error text stays in the log (R-RUN-14).
func TestAModuleErrorTextNeverReachesTheModel(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	_, err := e.call(t, "probe.fail")
	if err == nil || err.Error() != "probe.fail: the module failed (auth_failed). "+authHint {
		t.Fatalf("error %v", err)
	}
	e.cron(t, cronFails)
	ms := e.moduleEvents(t)
	if len(ms) != 1 || ms[0].Kind != "auth_failed" {
		t.Fatalf("events %+v", ms)
	}
	for _, m := range ms {
		if strings.Contains(m.Detail, "IGNORE") || strings.Contains(m.Detail, "money") {
			t.Fatalf("the module's text reached the event: %q", m.Detail)
		}
	}
	if !strings.Contains(e.logs.String(), "IGNORE ALL PREVIOUS") {
		t.Fatal("the module's text is not in the log for the owner to read")
	}
	if strings.Contains(e.logs.String(), "hunter2") {
		t.Fatal("a secret value reached the log")
	}
}

// A trap's error, which carries what the module wrote to stderr, reaches
// the model as the node's words only, and counts as an exception.
func TestATrapReachesTheModelInTheNodesWords(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	_, err := e.call(t, "probe.greedy")
	if err == nil || err.Error() != "probe.greedy: the module failed (module.exception). "+trapHint {
		t.Fatalf("error %v", err)
	}
	if fs := e.store.failures(); len(fs) != 1 || !fs[0].Exception || fs[0].Kind != "module.exception" {
		t.Fatalf("failures %+v", fs)
	}
}

// Five failures within an hour disable the module: off: true is written,
// its job deleted, its tools gone, and one module.crashloop event put; a
// sixth call does not run (design section 8.3).
func TestFiveFailuresInAnHourDisableTheModule(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	h := e.tool("probe.broken")
	call := func() error {
		_, err := h.Run(context.Background(), tool.Call{Name: "probe.broken", Args: json.RawMessage(`{}`), Admin: true})
		return err
	}
	for i := range 4 {
		if err := call(); err == nil {
			t.Fatalf("call %d succeeded", i)
		}
		e.now = e.now.Add(10 * time.Minute)
	}
	if e.tool("probe.broken") == nil {
		t.Fatal("disabled after four failures")
	}
	err := call()
	if err == nil || !strings.Contains(err.Error(), "the module is now off") {
		t.Fatalf("fifth call: %v", err)
	}
	if strings.Join(e.conf.offs, ",") != "local/probe:true:crashloop" {
		t.Fatalf("settings writes %v", e.conf.offs)
	}
	if js := e.jobs.all(); len(js) != 0 {
		t.Fatalf("jobs left %+v", js)
	}
	if len(e.host.Handlers()) != 0 {
		t.Fatal("tools left")
	}
	var crash int
	for _, m := range e.moduleEvents(t) {
		if m.Kind == "module.crashloop" {
			crash++
		}
	}
	if crash != 1 {
		t.Fatalf("%d crashloop events", crash)
	}
	runs := e.store.runs()
	if err := call(); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("sixth call: %v", err)
	}
	if err := e.host.RunCron(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if e.store.runs() != runs {
		t.Fatal("a disabled module ran")
	}
	checks, err := e.host.Status(context.Background(), status.Scope{})
	if err != nil || !hasLine(checks, texts.StatusModuleOff, texts.StatusReasonModuleCrashloop) {
		t.Fatalf("status %+v, %v", checks, err)
	}

	// The owner turns it on again: it runs with a fresh count.
	e.conf.mods[0].Off = false
	if err := e.host.Reload(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if err := call(); err == nil || strings.Contains(err.Error(), "now off") {
		t.Fatalf("after on: %v", err)
	}
}

// Failures spread over more than an hour do not disable a module.
func TestFailuresOutsideTheHourDoNotCount(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	for range 8 {
		if _, err := e.call(t, "probe.broken"); err == nil {
			t.Fatal("no error")
		}
		e.now = e.now.Add(16 * time.Minute)
	}
	if e.tool("probe.broken") == nil || len(e.conf.offs) != 0 {
		t.Fatal("disabled by failures spread over two hours")
	}
}

// fail runs a failing cron run (auth_failed); its failure is the point.
func (e *env) fail(t *testing.T) {
	t.Helper()
	e.cron(t, cronFails)
}

// A kind that fails on every run for a day escalates once, not once an hour
// (owner ruling of 2026-11-10); the key names the version and the episode.
func TestAKindFailingAllDayEscalatesOnce(t *testing.T) {
	a := active(t, "local/probe", probeManifest)
	e := newEnv(t, a)
	e.start(t)
	for range 48 {
		e.fail(t)
		e.now = e.now.Add(30 * time.Minute)
	}
	evs := e.inbox.taken()
	want := "module:local/probe:auth_failed:" + a.SHA256 + ":" + strconv.FormatInt(t0.UnixMilli(), 10)
	if len(evs) != 1 || evs[0].IdemKey != want || evs[0].Conv != "telegram:42" {
		t.Fatalf("events %+v, want one keyed %s", evs, want)
	}
}

// A successful run closes the episode: the next failure escalates again.
func TestASuccessClearsTheEpisode(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.fail(t)
	e.now = e.now.Add(time.Minute)
	if _, err := e.call(t, "probe.status"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	e.fail(t)
	e.fail(t)
	evs := e.inbox.taken()
	if len(evs) != 2 || evs[0].IdemKey == evs[1].IdemKey {
		t.Fatalf("events %+v, want two episodes", evs)
	}
}

// A cron run that ends well closes the episode too.
func TestACronSuccessClearsTheEpisode(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.fail(t)
	e.now = e.now.Add(time.Minute)
	e.cron(t, "")
	e.now = e.now.Add(time.Minute)
	e.fail(t)
	if evs := e.inbox.taken(); len(evs) != 2 {
		t.Fatalf("%d events, want 2", len(evs))
	}
}

// Two kinds escalate once each.
func TestTwoKindsEscalateOnceEach(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	for range 3 {
		e.fail(t)
		e.cron(t, cronBreaks)
		e.now = e.now.Add(time.Hour)
	}
	kinds := map[string]int{}
	for _, m := range e.moduleEvents(t) {
		kinds[m.Kind]++
	}
	if len(kinds) != 2 || kinds["auth_failed"] != 1 || kinds["module.format"] != 1 {
		t.Fatalf("events by kind %v", kinds)
	}
}

// A new version, a rollback and an "on" each give the module a fresh start:
// the same kind escalates again.
func TestAFreshStartClearsOpenKinds(t *testing.T) {
	ctx := context.Background()
	steps := map[string]func(*env) error{
		"new version": func(e *env) error {
			e.store.mu.Lock()
			e.store.active = []modstore.Active{active(t, "local/probe", strings.Replace(probeManifest, "version: 3", "version: 4", 1))}
			e.store.mu.Unlock()
			return e.host.Reload(ctx, "local/probe")
		},
		"rollback": func(e *env) error { _, err := e.host.Rollback(ctx, "probe"); return err },
		"on": func(e *env) error {
			if err := e.host.SetOff(ctx, "probe", true); err != nil {
				return err
			}
			return e.host.SetOff(ctx, "probe", false)
		},
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, active(t, "local/probe", probeManifest))
			e.start(t)
			e.fail(t)
			e.fail(t)
			if n := len(e.inbox.taken()); n != 1 {
				t.Fatalf("%d events before", n)
			}
			e.now = e.now.Add(time.Minute)
			if err := step(e); err != nil {
				t.Fatal(err)
			}
			e.fail(t)
			e.fail(t)
			if n := len(e.inbox.taken()); n != 2 {
				t.Fatalf("%d events after %s, want 2", n, name)
			}
		})
	}
}

// An open episode survives a restart: a new host over the same database
// does not escalate the kind again; a success closes it there as well.
func TestAnOpenEpisodeSurvivesARestart(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.fail(t)
	e.host = e.newHost()
	e.start(t)
	e.now = e.now.Add(3 * time.Hour)
	e.fail(t)
	if n := len(e.inbox.taken()); n != 1 {
		t.Fatalf("%d events after the restart, want 1", n)
	}
	e.host = e.newHost()
	e.start(t)
	if _, err := e.call(t, "probe.status"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	e.fail(t)
	if n := len(e.inbox.taken()); n != 2 {
		t.Fatalf("%d events after the recovery, want 2", n)
	}
}

// A success and a failure settling at once never leave the cache and the
// store disagreeing: afterwards one success always clears the episode, so
// the next failure escalates again.
func TestASuccessAndAFailureSettlingAtOnceAgree(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	h := e.tool("probe.status")
	ok := func() {
		h.Run(context.Background(), tool.Call{Name: h.Spec().Name, Args: json.RawMessage(`{}`), Admin: true}) //nolint:errcheck // either outcome is fine
	}
	e.state.Put("probe", "op_cron", cronFails)
	bad := func() { e.host.RunCron(context.Background(), "local/probe") } //nolint:errcheck // its failure is the point
	for range 40 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); ok() }()
		go func() { defer wg.Done(); bad() }()
		wg.Wait()
		e.now = e.now.Add(20 * time.Minute) // spread out: no crash loop
	}
	ok() // a success closes whatever is open, in the cache and the store
	before := len(e.inbox.taken())
	e.now = e.now.Add(time.Minute)
	bad()
	if n := len(e.inbox.taken()); n != before+1 {
		t.Fatalf("%d events after the recovery, want %d: the cache and the store disagree", n, before+1)
	}
}

// A kind the manifest does not escalate is counted but calls no model.
func TestAKindNotEscalatedPutsNoEvent(t *testing.T) {
	m := strings.Replace(probeManifest, "  - on: auth_failed\n", "  - on: stale\n", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	e.fail(t)
	if evs := e.inbox.taken(); len(evs) != 0 {
		t.Fatalf("events %+v", evs)
	}
	if len(e.store.failures()) != 1 {
		t.Fatal("the failure was not counted")
	}
}

// A module whose cell belongs to another module does not start, and says
// why in /status; it never writes that cell (ADR-0062).
func TestAModuleDoesNotTakeAnotherModulesCell(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	if err := e.cells.DefineOwned(context.Background(), cells.OwnedDef{ID: "probe.soc", Owner: "local/other", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	if len(e.host.Handlers()) != 0 || len(e.jobs.all()) != 0 {
		t.Fatal("the module started")
	}
	checks, _ := e.host.Status(context.Background(), status.Scope{})
	if !hasLine(checks, texts.StatusModuleStopped, texts.StatusReasonModuleCells) {
		t.Fatalf("status %+v", checks)
	}
	if err := e.host.Reload(context.Background(), "local/probe"); !errors.Is(err, modhost.ErrNotStarted) {
		t.Fatalf("reload: %v", err)
	}
}

// A module that is no longer active, or turned off, is unloaded on
// Reload: its tools and jobs go, its cells stay.
func TestReloadUnloadsARemovedModule(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.store.active = nil
	if err := e.host.Reload(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if len(e.host.Handlers()) != 0 || len(e.jobs.all()) != 0 {
		t.Fatal("the removed module still runs")
	}
	if c, err := e.cells.Get(context.Background(), "probe.soc"); err != nil || c.Owner != "local/probe" {
		t.Fatalf("cell %+v, %v", c, err)
	}
}

func hasLine(cs []status.Check, line, reason texts.Key) bool {
	for _, c := range cs {
		if c.Line == line && c.Reason == reason {
			return true
		}
	}
	return false
}

// A cron run writes only a cell the module owns: once its cell is
// ownerless (the module was removed and its cells freed), nothing is
// written to it, though the name is the module's own (ADR-0062).
func TestACronRunWritesOnlyAnOwnedCell(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	if _, err := e.cells.Disown(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if err := e.host.RunCron(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.cells.Get(context.Background(), "probe.soc"); c.Reported != nil {
		t.Fatalf("an ownerless cell was written: %s", c.Reported)
	}
}

// A module whose init fails does not start: the failure is counted, the
// reason is in /status, and Reload says so in the node's words.
func TestAFailedInitKeepsTheModuleStopped(t *testing.T) {
	m := strings.Replace(probeManifest, "config:\n", "config:\n  lifecycle: {type: string, default: fail}\n", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	if len(e.host.Handlers()) != 0 || len(e.jobs.all()) != 0 {
		t.Fatal("the module started")
	}
	if fs := e.store.failures(); len(fs) != 1 || fs[0].Kind != "module.exception" {
		t.Fatalf("failures %+v", fs)
	}
	checks, _ := e.host.Status(context.Background(), status.Scope{})
	if !hasLine(checks, texts.StatusModuleStopped, texts.StatusReasonModuleInit) {
		t.Fatalf("status %+v", checks)
	}
	err := e.host.Reload(context.Background(), "local/probe")
	if !errors.Is(err, modhost.ErrNotStarted) || strings.Contains(err.Error(), "boom") {
		t.Fatalf("reload: %v", err)
	}
}

// I2, abuse test: a module installed before the install refused the node's
// own secrets, and granted the bot token in its manifest, reads nothing.
func TestAModuleNeverReadsTheNodesOwnSecret(t *testing.T) {
	m := strings.Replace(probeManifest, "secret.get: [PROBE_TOKEN]", "secret.get: [PROBE_TOKEN, TELEGRAM_BOT_TOKEN]", 1)
	m = strings.Replace(m, "  - tool: greedy\n", "  - tool: peek\n    about: Reads a secret.\n    output: {value: string, ok: boolean}\n  - tool: greedy\n", 1)
	m = strings.Replace(m, "config:\n", "config:\n  op_tool_peek: {type: string, default: '{\"op\":\"secret\",\"name\":\"TELEGRAM_BOT_TOKEN\"}'}\n", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	res, err := e.call(t, "probe.peek")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, botToken) || !strings.Contains(res.Text, `"ok":false`) {
		t.Fatalf("the module read the node's own secret: %s", res.Text)
	}
}

// A store that cannot be read at start does not stop the node: Start logs it
// and /status says the local modules were not loaded; a later Reload that
// can read them starts them.
func TestStartDoesNotStopTheNodeWhenTheStoreCannotBeRead(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.store.failActive = errors.New("disk I/O error")
	e.start(t)
	checks, err := e.host.Status(context.Background(), status.Scope{})
	if err != nil || len(checks) != 1 || checks[0].Line != texts.StatusModulesUnread || checks[0].Since != t0 {
		t.Fatalf("status %+v, %v", checks, err)
	}
	if !strings.Contains(e.logText(), "the local modules are not loaded") {
		t.Fatalf("log:\n%s", e.logText())
	}
	e.store.failActive = nil
	if err := e.host.Reload(context.Background(), "local/probe"); err != nil {
		t.Fatal(err)
	}
	if checks, _ := e.host.Status(context.Background(), status.Scope{}); hasLine(checks, texts.StatusModulesUnread, "") {
		t.Fatalf("status %+v", checks)
	}
}

// Jobs of a removed module that cannot be deleted are logged; Start goes on.
func TestStartGoesOnWhenOrphanJobsCannotBeCleared(t *testing.T) {
	e := newEnv(t)
	e.store.removed = []string{"local/old"}
	e.jobs.failDelete = errors.New("database is locked")
	e.start(t)
	if !strings.Contains(e.logText(), "the jobs of removed modules are not cleared") {
		t.Fatalf("log:\n%s", e.logText())
	}
}

// Hints the node adds to a failure, by kind.
const (
	authHint = "The service refused the module's login: its secrets or settings need checking; " +
		"module.status with name probe shows its settings and health."
	trapHint = "It stopped with an error: a trap, or an answer the node cannot read."
)

// The node's words for a failed tool call, golden per kind: the kind, then
// the node's hint for it, never the module's text (R-RUN-14).
func TestAFailedToolCallHasTheNodesHint(t *testing.T) {
	m := strings.Replace(probeManifest, "  - tool: greedy\n", "  - tool: crash\n    about: Throws.\n    output: {x: integer}\n"+
		"  - tool: slow\n    about: Spins.\n    output: {x: integer}\n  - tool: greedy\n", 1)
	m = strings.Replace(m, "  op_tool_greedy:", "  op_tool_crash: {type: string, default: '{\"op\":\"nope\"}'}\n"+
		"  op_tool_slow: {type: string, default: '{\"op\":\"spin\"}'}\n  op_tool_greedy:", 1)
	m = strings.Replace(m, "timeout: 5s", "timeout: 1s", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	cases := map[string]string{
		"probe.fail":   "probe.fail: the module failed (auth_failed). " + authHint,
		"probe.broken": "probe.broken: field value: expected integer, got string. " + formatHint,
		"probe.crash": "probe.crash: the module failed (module.exception). The module failed; the node's log has the details, " +
			"and module.status with name probe shows its settings and health.",
		"probe.slow":   "probe.slow: the module failed (module.exception). It ran past its time limit.",
		"probe.greedy": "probe.greedy: the module failed (module.exception). " + trapHint,
	}
	for name, want := range cases {
		_, err := e.call(t, name)
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", name, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "IGNORE") {
			t.Errorf("%s: the module's text reached the model", name)
		}
		e.now = e.now.Add(20 * time.Minute)
	}
	if evs := e.inbox.taken(); len(evs) != 0 {
		t.Fatalf("tool calls escalated: %+v", evs)
	}
	if hn := e.host.Hint("probe", modstore.Health("stale")); hn != "The module's data source has not updated recently." {
		t.Fatalf("stale hint %q", hn)
	}
}

// I3, abuse test: a module that writes its granted secret into its log and
// its error text never gets the value into the node's log or the answer:
// the module's granted secrets are redacted from every module text the host
// logs, on a run, a contract break and a health check (Global Constraints).
func TestAModulesGrantedSecretNeverReachesTheLog(t *testing.T) {
	m := strings.Replace(probeManifest, "  - tool: greedy\n", "  - tool: leak\n    about: Leaks.\n    output: {x: integer}\n  - tool: greedy\n", 1)
	m = strings.Replace(m, "  op_tool_greedy:", "  op_tool_leak: {type: string, default: '{\"op\":\"leak\",\"name\":\"PROBE_TOKEN\"}'}\n"+
		"  lifecycle: {type: string, default: leak}\n  op_tool_greedy:", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	if len(e.host.Handlers()) == 0 {
		t.Fatal("the module did not start")
	}
	_, err := e.call(t, "probe.leak")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("answer %v", err)
	}
	e.cron(t, `{"op":"fail","value":"token hunter2","name":"soc","json":"87"}`)
	e.cron(t, `{"op":"leak","name":"PROBE_TOKEN"}`)
	card, err := e.host.Card(context.Background(), "probe")
	if err != nil || card.Health != "module.exception" {
		t.Fatalf("card health %q, %v", card.Health, err)
	}
	for _, ev := range e.moduleEvents(t) {
		if strings.Contains(ev.Detail, "hunter2") {
			t.Fatalf("event %+v", ev)
		}
	}
	log := e.logText()
	if strings.Contains(log, "hunter2") {
		t.Fatalf("a granted secret reached the log:\n%s", log)
	}
	if strings.Count(log, "token [redacted]") < 4 {
		t.Fatalf("the module's texts are not in the log, redacted:\n%s", log)
	}
}
