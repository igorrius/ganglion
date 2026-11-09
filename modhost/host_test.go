package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modhost"
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

// A contract break gives the node's words, never the value, counts a
// failure and escalates module.format (R-RUN-14).
func TestAContractBreakIsTheNodesError(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	res, err := e.call(t, "probe.broken")
	if err == nil || err.Error() != "probe.broken: field value: expected integer, got string" || res.Text != "" {
		t.Fatalf("result %+v, %v", res, err)
	}
	fs := e.store.failures()
	if len(fs) != 1 || fs[0].Kind != "module.format" || fs[0].Exception {
		t.Fatalf("failures %+v", fs)
	}
	ms := e.moduleEvents(t)
	if len(ms) != 1 || ms[0].Kind != "module.format" || ms[0].Detail != "probe.broken: field value: expected integer, got string" ||
		ms[0].Module != "local/probe" || ms[0].Version != "3" || ms[0].Principal != owner || ms[0].Transport != "telegram" {
		t.Fatalf("events %+v", ms)
	}
}

// A module's failure reaches the model and the owner in the node's words
// only: the module's own error text stays in the log (R-RUN-14).
func TestAModuleErrorTextNeverReachesTheModel(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	_, err := e.call(t, "probe.fail")
	if err == nil || err.Error() != "probe.fail: the module failed (auth_failed)" {
		t.Fatalf("error %v", err)
	}
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
	if err == nil || err.Error() != "probe.greedy: the module failed (module.exception)" {
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

// Two auth_failed in the same hour put one event: the same IdemKey; the
// next hour may put another (ruling 10 of the M4b plan).
func TestOneEscalationOfAKindAnHour(t *testing.T) {
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	e.call(t, "probe.fail") //nolint:errcheck // the failure is the point
	e.now = e.now.Add(20 * time.Minute)
	e.call(t, "probe.fail") //nolint:errcheck // the failure is the point
	evs := e.inbox.taken()
	if len(evs) != 1 || evs[0].IdemKey != "module:local/probe:auth_failed:2026-11-09T14" || evs[0].Conv != "telegram:42" {
		t.Fatalf("events %+v", evs)
	}
	e.now = e.now.Add(time.Hour)
	e.call(t, "probe.fail") //nolint:errcheck // the failure is the point
	if evs := e.inbox.taken(); len(evs) != 2 {
		t.Fatalf("next hour: %d events", len(evs))
	}
}

// A kind the manifest does not escalate is counted but calls no model.
func TestAKindNotEscalatedPutsNoEvent(t *testing.T) {
	m := strings.Replace(probeManifest, "  - on: auth_failed\n", "  - on: stale\n", 1)
	e := newEnv(t, active(t, "local/probe", m))
	e.start(t)
	e.call(t, "probe.fail") //nolint:errcheck // the failure is the point
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
