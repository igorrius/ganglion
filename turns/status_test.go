package turns_test

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/turns"
)

var now = time.Date(2026, 11, 8, 13, 0, 0, 0, time.UTC)

func scope() status.Scope { return status.Scope{Now: now, Location: time.UTC} }

// record stores a turn with route at.
func record(t *testing.T, l *turns.Log, id string, at time.Time, out turns.Outcome, route ...turns.Attempt) {
	t.Helper()
	if err := l.Record(t.Context(), turns.Turn{EventID: id, Conv: "c", Started: at, Finished: at, Outcome: out, Route: route}); err != nil {
		t.Fatal(err)
	}
}

func clock(h, m int) time.Time { return time.Date(2026, 11, 8, h, m, 0, 0, time.UTC) }

// byModel is the models' lines by model name.
func byModel(t *testing.T, l *turns.Log) map[string]status.Check {
	t.Helper()
	out := map[string]status.Check{}
	for _, c := range l.Status(t.Context(), scope()) {
		if c.Section == status.SectionModels {
			out[c.Vars["model"]] = c
		}
	}
	return out
}

func TestStatusOfModels(t *testing.T) {
	l := newLog(t)
	record(t, l, "1", clock(12, 0), turns.OutcomeOK, turns.Attempt{Provider: "p", Model: "a", Error: "rate_limit"}, turns.Attempt{Provider: "p", Model: "b"})
	record(t, l, "2", clock(12, 40), turns.OutcomeOK, turns.Attempt{Provider: "p", Model: "a"})
	record(t, l, "3", clock(12, 50), turns.OutcomeOK, turns.Attempt{Provider: "p", Model: "b", Error: "timeout"}, turns.Attempt{Provider: "p", Model: "a"})
	record(t, l, "old", now.Add(-8*24*time.Hour), turns.OutcomeOK, turns.Attempt{Provider: "p", Model: "z"})
	got := byModel(t, l)
	if a := got["a"]; a.State != status.OK || a.Line != texts.StatusModelAnswered || a.Vars["time"] != "12:50" {
		t.Errorf("a: %+v", a)
	}
	b := got["b"]
	if b.State != status.Warn || b.Reason != texts.StatusReasonModelFailed || b.Vars["kind"] != "timeout" || b.Vars["time"] != "12:00" || !b.Since.Equal(clock(12, 50)) {
		t.Errorf("b: %+v", b)
	}
	if _, ok := got["z"]; ok {
		t.Error("a model seen 8 days ago is shown")
	}
}

func TestStatusEveryModelFailing(t *testing.T) {
	l := newLog(t)
	record(t, l, "1", clock(12, 0), turns.OutcomeNoModel, turns.Attempt{Provider: "p", Model: "a", Error: "timeout"}, turns.Attempt{Provider: "p", Model: "b", Error: "auth"})
	for m, c := range byModel(t, l) {
		if c.State != status.Fail || c.Line != texts.StatusModelSilent {
			t.Errorf("%s: %+v", m, c)
		}
	}
}

func TestStatusFailedTurns(t *testing.T) {
	l := newLog(t)
	record(t, l, "1", clock(9, 0), turns.OutcomeError)
	record(t, l, "2", clock(10, 0), turns.OutcomeNoModel)
	record(t, l, "3", clock(11, 0), turns.OutcomeError)
	record(t, l, "4", clock(11, 30), turns.OutcomeOK)
	record(t, l, "old", now.Add(-25*time.Hour), turns.OutcomeError)
	var failed status.Check
	for _, c := range l.Status(t.Context(), scope()) {
		if c.Line == texts.StatusTurnsFailed {
			failed = c
		}
	}
	if failed.State != status.Warn || failed.Section != status.SectionProblems || failed.Vars["n"] != "3" {
		t.Errorf("failed: %+v", failed)
	}
	l = newLog(t)
	for _, c := range l.Status(t.Context(), scope()) {
		if c.Line == texts.StatusTurnsFailed && (c.State != status.OK || c.Vars["n"] != "0") {
			t.Errorf("none: %+v", c)
		}
	}
}
