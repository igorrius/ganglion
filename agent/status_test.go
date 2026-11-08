package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/turns"
)

// fakeSource answers /status with fixed lines.
type fakeSource struct {
	name   string
	checks []status.Check
}

func (f fakeSource) Name() string                                        { return f.name }
func (f fakeSource) Status(context.Context, status.Scope) []status.Check { return f.checks }

// statusEnv is the test env with the given sources.
func statusEnv(t *testing.T, cfg Config, srcs ...status.Source) *env {
	t.Helper()
	e := newEnv(t, cfg, nil)
	e.deps.StatusSources = srcs
	e.a = New(overDefaults(cfg), e.deps)
	return e
}

// statusReply is the one message /status answers with.
func statusReply(t *testing.T, e *env) string {
	t.Helper()
	out := e.turn(t, message(owner, "Igor", "/status"))
	if len(out) != 1 {
		t.Fatalf("out %+v", out)
	}
	return out[0].Text
}

func TestStatusShowsVerdictSectionsAndReasons(t *testing.T) {
	since := t0.Add(-time.Hour)
	e := statusEnv(t, Config{}, fakeSource{name: "skills", checks: []status.Check{
		{Section: status.SectionNode, State: status.OK, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": "x"}},
		{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusNoAnswer,
			Vars: texts.Vars{"component": "my_skill"}, Since: since},
	}})
	reply := statusReply(t, e)
	for _, want := range []string{"⚠️ Needs a look: 1", "## Node", "## Models", "## Off and errors",
		`my\_skill: no answer`, "since 09:00", "This session: smart, cheap"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply lacks %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "## Delivery") {
		t.Errorf("a section with no line is shown:\n%s", reply)
	}
	if strings.Index(reply, "## Node") > strings.Index(reply, "## Models") {
		t.Errorf("sections out of order:\n%s", reply)
	}
}

// /status spends no model and joins no history; a family member gets the refusal.
func TestStatusIsForAdminsAndLeavesNoNote(t *testing.T) {
	e := statusEnv(t, Config{})
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	if reply := statusReply(t, e); !strings.HasPrefix(reply, "✅ All well") {
		t.Errorf("reply:\n%s", reply)
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/status reached the model or the history")
	}
	e.admin = false
	out := e.turn(t, message("telegram:user:7", "Olena", "/status"))
	if len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("a family member got %+v", out)
	}
}

func TestStatusSessionLineNamesTheFallback(t *testing.T) {
	e := statusEnv(t, Config{})
	err := e.deps.Turns.Record(context.Background(), turns.Turn{EventID: "ev1", Conv: "telegram:42", Started: t0, Finished: t0,
		Outcome: turns.OutcomeOK, Route: []turns.Attempt{{Provider: "p1", Model: "smart", Error: "timeout"}, {Provider: "p2", Model: "cheap"}}})
	if err != nil {
		t.Fatal(err)
	}
	reply := statusReply(t, e)
	if !strings.Contains(reply, "This session: smart, cheap; the last turn was answered by cheap") || !strings.HasPrefix(reply, "⚠️") {
		t.Errorf("reply:\n%s", reply)
	}
}

func TestStatusDayAgainstTheLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit, turns  int
		line, verdict string
	}{
		{"near", 10, 9, "turns 9/10", "⚠️"},
		{"reached", 10, 10, "a daily limit is reached", "❌"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := statusEnv(t, Config{DailyTurns: tc.limit})
			for i := range tc.turns {
				err := e.deps.Turns.Record(context.Background(), turns.Turn{EventID: "ev" + string(rune('a'+i)), Conv: "telegram:42",
					Principal: owner, Started: t0, Finished: t0, Outcome: turns.OutcomeOK, CostUSD: 0.5})
				if err != nil {
					t.Fatal(err)
				}
			}
			reply := statusReply(t, e)
			if !strings.Contains(reply, tc.line) || !strings.HasPrefix(reply, tc.verdict) {
				t.Errorf("reply:\n%s", reply)
			}
			if tc.name == "near" && !strings.Contains(reply, "past 80% of a daily limit") {
				t.Errorf("no reason:\n%s", reply)
			}
		})
	}
}

func TestStatusInTheSendersLanguage(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.Languages = &fakeLanguages{lang: map[string]texts.Lang{owner: "ru"}}
	e.deps.Translate = func(texts.Lang) {}
	e.a = New(overDefaults(Config{}), e.deps)
	if reply := statusReply(t, e); !strings.Contains(reply, "## Модели") || !strings.Contains(reply, "Эта сессия: smart, cheap") {
		t.Errorf("reply:\n%s", reply)
	}
}

func TestOfLimit(t *testing.T) {
	if got := ofLimit(9, 0); got != "9" {
		t.Errorf("off: %q", got)
	}
	if got := ofLimit(9, 10); got != "9/10" {
		t.Errorf("on: %q", got)
	}
}
