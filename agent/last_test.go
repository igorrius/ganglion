package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

func TestShowTurn(t *testing.T) {
	at := time.Date(2026, 11, 5, 19, 9, 0, 0, time.UTC)
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	whole := turns.Turn{Started: at, Finished: at.Add(14 * time.Second), Steps: 3, Outcome: turns.OutcomeOK, Joined: 1,
		Route: []turns.Attempt{{Provider: "claude-cli", Model: "sonnet", Error: turns.EndedForAddition}, {Provider: "claude-cli", Model: "sonnet"}, {Provider: "claude-cli", Model: "sonnet"}},
		Tools: []turns.ToolUse{{Name: "web.search", OK: true, Ms: 1200}, {Name: "web.fetch", OK: true, Ms: 3400, Note: turns.NoteApproved},
			{Name: "web.fetch", Ms: 800}, {Name: "schedule.create", OK: true, Ms: 2, Note: turns.NoteAsked}},
		InputTokens: 12400, CacheReadTokens: 9800, OutputTokens: 640, CostUSD: 0.01}
	for name, tc := range map[string]struct {
		turn turns.Turn
		now  time.Time
		want string
	}{
		"whole": {whole, at, "Last turn, 21:09 · 14.0 s · ok\n" +
			"Model: sonnet (claude-cli)\n" +
			"Steps: 3\n" +
			"Tools:\n" +
			"  ✓ web.search       1.2 s\n" +
			"  ✓ web.fetch        3.4 s  approved\n" +
			"  ✗ web.fetch        0.8 s\n" +
			"  · schedule.create  0.0 s  asked\n" +
			"Joined: 1 message\n" +
			"Tokens: 12,400 in (9,800 cached), 640 out\n" +
			"Cost: $0.0100"},
		"another day, a fallback, a failure": {turns.Turn{Started: at, Finished: at, Steps: 1, Outcome: turns.OutcomeError, Error: "boom",
			Route: []turns.Attempt{{Provider: "cli", Model: "sonnet", Error: "timeout"}, {Provider: "or", Model: "deepseek"}}, Joined: 2}, at.Add(24 * time.Hour),
			"Last turn, 5 Nov 21:09 · 0.0 s · error\n" +
				"Error: boom\n" +
				"Model: sonnet (cli) failed (timeout), then deepseek (or)\n" +
				"Steps: 1\n" +
				"Joined: 2 messages\n" +
				"Tokens: 0 in, 0 out\n" +
				"Cost: $0.0000"},
	} {
		if got := showTurn(tc.turn, kyiv, tc.now); got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, tc.want)
		}
	}
}

// A long list stops; a rerun's call, whose time is lost, shows none.
func TestShowTurnTools(t *testing.T) {
	var tools []turns.ToolUse
	for range maxToolsShown + 2 {
		tools = append(tools, turns.ToolUse{Name: "x", OK: true, Note: turns.NoteApproved})
	}
	got := showTurn(turns.Turn{Tools: tools}, time.UTC, time.Time{})
	if strings.Count(got, "✓ x") != maxToolsShown || !strings.Contains(got, "… and 2 more") {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(got, "✓ x  approved") {
		t.Fatalf("a call with no time: %s", got)
	}
}

// An error can quote anything: it stays one short line, secrets gone, and inside
// the block (R-OBS-10).
func TestShowTurnHostileError(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	if err := e.deps.Turns.Record(context.Background(), turns.Turn{EventID: "E1", Conv: "telegram:42", Started: e.now, Finished: e.now, Outcome: turns.OutcomeError,
		Error: "```\n\n[click](http://evil.example) token=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789\n" + strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	out := e.turn(t, message(owner, "Igor", "/last"))
	if len(out) != 1 {
		t.Fatalf("out %+v", out)
	}
	body, ok := strings.CutPrefix(out[0].Text, "```\n")
	body, closed := strings.CutSuffix(body, "\n```")
	if !ok || !closed || strings.Contains(body, "```") {
		t.Fatalf("not one block: %q", out[0].Text)
	}
	if strings.Contains(body, "sk-ant-") || len(body) > 500 {
		t.Fatalf("the error came through: %q", body)
	}
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "[click]") {
			t.Fatalf("the error broke its line: %q", body)
		}
	}
}

// /last spends no model, joins no history and shows the turn before it.
func TestLastCommandShowsTheTurnBefore(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	for range 2 { // the second sees past the first
		out := e.turn(t, message(owner, "Igor", "/last"))
		if len(out) != 1 || !strings.Contains(out[0].Text, "Last turn, ") || !strings.Contains(out[0].Text, "· ok") || !strings.Contains(out[0].Text, "Steps: 1") {
			t.Fatalf("out %+v", out)
		}
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/last reached the model or the history")
	}
}

func TestLastCommandWithNoTurn(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	if out := e.turn(t, message(owner, "Igor", "/last")); len(out) != 1 || out[0].Text != replyNoTurn {
		t.Fatalf("out %+v", out)
	}
}

// Someone who is not an admin sees nothing of the turn and spends no model.
func TestLastCommandIsForAdmins(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	e.admin = false
	calls := len(e.smart.requests())
	out := e.turn(t, message("telegram:user:7", "Olena", "/last"))
	if len(out) != 1 || out[0].Text != replyTasksNotAvailable || len(e.smart.requests()) != calls {
		t.Fatalf("out %+v", out)
	}
}

// A forward is never a command, as for the others.
func TestLastCommandInAForwardIsNotOne(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("That is a forward."))
	ev := outsideMessage(event.Message{Text: "/last", From: owner, Name: "Igor", Forwarded: true})
	if out := e.turn(t, ev); len(out) != 1 || strings.Contains(out[0].Text, "Last turn") {
		t.Fatalf("out %+v", out)
	}
}

// The turn says what each confirmed call did: asked in its own turn; approved,
// declined or expired in the one that went on (R-OBS-10).
func TestTurnRecordsHowACallWasDecided(t *testing.T) {
	lastTool := func(t *testing.T, e *confirmEnv) turns.ToolUse {
		t.Helper()
		got, err := e.deps.Turns.Last(context.Background(), "telegram:42")
		if err != nil || len(got.Tools) == 0 {
			t.Fatalf("last %+v, %v", got, err)
		}
		return got.Tools[len(got.Tools)-1]
	}
	t.Run("asked", func(t *testing.T) {
		e := newConfirmEnv(t, newSideTool())
		e.ask(t, "schedule.create")
		if u := lastTool(t, e); u.Note != turns.NoteAsked || u.Name != "schedule.create" {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("approved", func(t *testing.T) {
		e := newConfirmEnv(t, newSideTool())
		q, _ := e.ask(t, "schedule.create")
		e.smart.push(say("Done."))
		e.turn(t, press(owner, q.Buttons[0].ID, "77"))
		if u := lastTool(t, e); u.Note != turns.NoteApproved || !u.OK {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("approved, failed", func(t *testing.T) {
		tl := &failTool{sideTool: *newSideTool(), err: errors.New("boom")}
		e := newConfirmEnv(t, tl)
		q, _ := e.ask(t, "schedule.create")
		e.smart.push(say("It failed."))
		e.turn(t, press(owner, q.Buttons[0].ID, "77"))
		if u := lastTool(t, e); u.Note != turns.NoteApproved || u.OK {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("declined", func(t *testing.T) {
		e := newConfirmEnv(t, newSideTool())
		q, _ := e.ask(t, "schedule.create")
		e.smart.push(say("OK."))
		e.turn(t, press(owner, q.Buttons[1].ID, "77"))
		if u := lastTool(t, e); u.Note != turns.NoteDeclined {
			t.Fatalf("%+v", u)
		}
	})
	t.Run("expired", func(t *testing.T) {
		e := newConfirmEnv(t, newSideTool())
		e.ask(t, "schedule.create")
		e.now = e.now.Add(pending.DefaultTTL)
		e.smart.push(say("You did not answer."))
		e.turn(t, expired(t, e))
		if u := lastTool(t, e); u.Note != turns.NoteExpired {
			t.Fatalf("%+v", u)
		}
	})
}

// A message that joined the turn is counted on it (R-OBS-10).
func TestTurnRecordsWhatJoined(t *testing.T) {
	e := newAddEnv(t, Config{}, newClock())
	first := from("question")
	e.smart.push(sayAfter(func() { e.put(t, from("addition")) }, "first"), say("second"))
	e.put(t, first)
	e.next(t)
	got, err := e.deps.Turns.Last(context.Background(), "telegram:42")
	if err != nil || got.Joined != 1 {
		t.Fatalf("last %+v, %v", got, err)
	}
}
