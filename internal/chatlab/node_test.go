//go:build lab

package chatlab

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const quietLimit = 30 * time.Second

func startScripted(t *testing.T, s Scenario) *Node {
	t.Helper()
	s.Model = ModelScripted
	if s.Persona == "" {
		s.Persona = PersonaDefault
	}
	n, err := Start(context.Background(), NodeOptions{Scenario: s, Tokens: &Counter{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return n
}

func sayQuiet(t *testing.T, n *Node, text string) {
	t.Helper()
	ctx := context.Background()
	if err := n.Peer().Say(ctx, text); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
}

func texts(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

func shows(ms []Message, part string) bool {
	return slices.ContainsFunc(ms, func(m Message) bool { return strings.Contains(m.Text, part) })
}

func journalCount(t *testing.T, db *sql.DB, tool, decision string) int {
	t.Helper()
	var c int
	if err := db.QueryRow(`SELECT count(*) FROM tool_calls WHERE tool = ? AND decision = ?`, tool, decision).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func reminderArgs(t *testing.T, at time.Time) string {
	t.Helper()
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]string{"kind": "reminder", "text": "Call the plumber.", "at": at.In(kyiv).Format("2006-01-02 15:04")})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScriptedNodeAnswers(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{{Say: "Hello from the script."}}})
	sayQuiet(t, n, "hi")
	if !shows(n.Peer().Messages(), "Hello from the script.") {
		t.Fatalf("messages %q", texts(n.Peer().Messages()))
	}
}

func TestScriptedGuardIsSeparate(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{
		{Tool: "memory.write", Args: `{"title":"Boiler","body":"The boiler holds 80 litres."}`},
		{Say: "Stored."},
	}})
	sayQuiet(t, n, "Remember that my boiler holds 80 litres")
	if !shows(n.Peer().Messages(), "Stored.") {
		t.Fatalf("the guard took a script step: %q", texts(n.Peer().Messages()))
	}
	if got := journalCount(t, n.DB(), "memory.write", "allowed"); got != 1 {
		t.Fatalf("memory.write allowed %d times", got)
	}
	files, err := filepath.Glob(filepath.Join(n.Workspace(), "memory", "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("memory files %v, %v", files, err)
	}
}

func TestNodeCountsTokens(t *testing.T) {
	var c Counter
	n, err := Start(context.Background(), NodeOptions{Tokens: &c, Scenario: Scenario{Name: "x", Model: ModelScripted, Persona: PersonaDefault,
		Script: []ScriptStep{{Say: "ok"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Stop(context.Background()) })
	sayQuiet(t, n, "hi")
	if c.Total() == 0 {
		t.Fatal("no tokens counted")
	}
}

func TestNodeIsIsolated(t *testing.T) {
	a := startScripted(t, Scenario{Name: "a", Script: []ScriptStep{{Say: "ok"}}})
	b := startScripted(t, Scenario{Name: "b", Script: []ScriptStep{{Say: "ok"}}})
	if a.Workspace() == b.Workspace() || a.LogDir() == b.LogDir() {
		t.Fatalf("shared directories: %s, %s", a.Workspace(), a.LogDir())
	}
	for _, p := range []string{a.Workspace(), a.LogDir()} {
		if !strings.HasPrefix(p, os.TempDir()) {
			t.Errorf("%s is outside the temporary directory", p)
		}
	}
	sayQuiet(t, a, "only here")
	if shows(b.Peer().Messages(), "ok") {
		t.Fatal("the other node's peer saw the message")
	}
}

func TestAdvanceConfirmations(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{
		{Tool: "schedule.create", Args: reminderArgs(t, time.Now().Add(24*time.Hour))},
		{Say: "I asked the owner."},
		{Say: "It expired."},
	}})
	sayQuiet(t, n, "Set a reminder")
	if !slices.ContainsFunc(n.Peer().Messages(), func(m Message) bool { return slices.Contains(m.Buttons, "Approve") }) {
		t.Fatalf("no question: %+v", n.Peer().Messages())
	}
	ctx := context.Background()
	if err := n.Advance("confirmations", 16*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	if got := journalCount(t, n.DB(), "schedule.create", "expired"); got != 1 {
		t.Fatalf("expired %d times", got)
	}
	closed := slices.ContainsFunc(n.Peer().Messages(), func(m Message) bool {
		return m.Replaced && strings.HasSuffix(m.Text, "Expired.") && len(m.Buttons) == 0
	})
	if !closed {
		t.Fatalf("the question did not close: %+v", n.Peer().Messages())
	}
}

func TestAdvanceScheduler(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{
		{Tool: "schedule.create", Args: reminderArgs(t, time.Now().Add(time.Hour))},
		{Say: "I asked the owner."},
		{Say: "It is set."},
	}})
	sayQuiet(t, n, "Set a reminder")
	ctx := context.Background()
	if err := n.Peer().Press(ctx, "Approve"); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	if err := n.Advance("scheduler", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	fired := slices.ContainsFunc(n.Peer().Messages(), func(m Message) bool {
		return strings.Contains(m.Text, "Call the plumber.") && len(m.Buttons) == 0 && !m.Replaced
	})
	if !fired {
		t.Fatalf("the reminder did not fire: %q", texts(n.Peer().Messages()))
	}
}

func TestAdvanceUnknownClock(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{{Say: "ok"}}})
	if err := n.Advance("sundial", time.Hour); err == nil {
		t.Fatal("an unknown clock moved")
	}
}

func TestPageIsServedAtItsHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tips.html"), []byte("<html><body><p>Clean the fan filter monthly.</p></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := startScripted(t, Scenario{Name: "x", Dir: dir, Pages: []Page{{URL: "http://solar-notes.example.net/tips", File: "tips.html"}},
		Script: []ScriptStep{{Tool: "web.fetch", Args: `{"url":"http://solar-notes.example.net/tips"}`}, {Say: "I asked."}, {Say: "Read it."}}})
	sayQuiet(t, n, "Read the page")
	ctx := context.Background()
	if err := n.Peer().Press(ctx, "Approve"); err != nil { // a host outside the search results asks first
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	var head string
	if err := n.DB().QueryRow(`SELECT result_head FROM tool_calls WHERE tool = 'web.fetch' AND decision = 'approved'`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(head, "Clean the fan filter") {
		t.Fatalf("result %q", head)
	}
}

func TestSkillIsCopied(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "disk-report")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "report.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := startScripted(t, Scenario{Name: "x", Dir: dir, Skills: []string{"skills/disk-report"}, Script: []ScriptStep{{Say: "ok"}}})
	info, err := os.Stat(filepath.Join(n.Workspace(), "skills", "disk-report", "report.sh"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("copied script: %v, %v", info, err)
	}
}

func TestStopRemovesTheDirectory(t *testing.T) {
	n, err := Start(context.Background(), NodeOptions{Tokens: &Counter{}, Scenario: Scenario{Name: "x", Model: ModelScripted, Persona: PersonaDefault,
		Script: []ScriptStep{{Say: "ok"}}}})
	if err != nil {
		t.Fatal(err)
	}
	ws := n.Workspace()
	if err := n.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(ws)); !os.IsNotExist(err) {
		t.Fatalf("the directory stayed: %v", err)
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("a second stop: %v", err)
	}
}

func TestQuietLimit(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{{Say: "ok"}}})
	if _, err := n.DB().Exec(`INSERT INTO outbox (id, idem_key, transport, conv, body, created_ms) VALUES ('stuck', 'stuck', 'none', 'none:1', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(context.Background(), time.Second); err == nil {
		t.Fatal("quiet with a message that will never leave")
	}
}

// Run now shows its question by editing the pressed message, not by sending
// one (found in review).
func TestRunNowQuestionClosesItself(t *testing.T) {
	create := `{"kind":"task","cron":"0 * * * *","text":"Read the page.","grant":{"tools":["message.send"]}}`
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{
		{Tool: "schedule.create", Args: create}, {Say: "I asked."}, {Say: "It is set."},
		{Tool: "schedule.list", Args: `{}`}, {Say: "Here they are."},
	}})
	ctx := context.Background()
	sayQuiet(t, n, "Create a task")
	if err := n.Peer().Press(ctx, "Approve"); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	sayQuiet(t, n, "Show my jobs")
	if err := n.Peer().Press(ctx, "Run now"); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	asking := func(m Message) bool { return slices.Contains(m.Buttons, "Approve") }
	if !slices.ContainsFunc(n.Peer().Messages(), asking) {
		t.Fatalf("no question: %+v", n.Peer().Messages())
	}
	if err := n.Advance("confirmations", 16*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(n.Peer().Messages(), asking) {
		t.Fatalf("the question is still open: %+v", n.Peer().Messages())
	}
	if !shows(n.Peer().Messages(), "Expired.") {
		t.Fatalf("no Expired: %q", texts(n.Peer().Messages()))
	}
}

func TestReadView(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{
		{Tool: "memory.write", Args: `{"title":"Boiler","body":"The boiler holds 80 litres."}`},
		{Say: "Stored it."},
		{Tool: "schedule.create", Args: reminderArgs(t, time.Now().Add(time.Hour))},
		{Say: "I asked."},
		{Say: "Set."},
	}})
	sayQuiet(t, n, "Remember my boiler")
	sayQuiet(t, n, "Set a reminder")
	ctx := context.Background()
	if err := n.Peer().Press(ctx, "Approve"); err != nil {
		t.Fatal(err)
	}
	if err := n.Quiet(ctx, quietLimit); err != nil {
		t.Fatal(err)
	}
	last := n.Peer().Messages()[1:2]
	v, err := ReadView(ctx, n, last)
	if err != nil {
		t.Fatal(err)
	}
	if got := Check(Facts{
		Journal: []JournalFact{{Tool: "memory.write", Decision: "allowed"}, {Tool: "schedule.create", Decision: "approved"}},
		History: &Text{Contains: "Remember my boiler"},
		Memory:  &Text{Contains: "80 litres"},
		Jobs:    &JobFact{Kind: "reminder", Count: 1},
		Reply:   &Text{Contains: last[0].Text},
	}, v); len(got) != 0 {
		t.Fatalf("%v\nview %+v", got, v.Jobs)
	}
	if v.Jobs["system"] != 0 {
		t.Fatalf("system jobs counted: %+v", v.Jobs)
	}
}

// The model must read the same time as the scheduler, or it reports a
// reminder as fired early: the first run of R50 found that.
func TestAdvanceSchedulerMovesTheModelsClock(t *testing.T) {
	n := startScripted(t, Scenario{Name: "x", Script: []ScriptStep{{Say: "one"}, {Say: "two"}}})
	sayQuiet(t, n, "first")
	if err := n.Advance("scheduler", 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	sayQuiet(t, n, "second")
	reqs := n.scripted.Requests()
	last := reqs[len(reqs)-1].Messages
	content, _ := last[len(last)-1]["content"].(string)
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	moved := time.Now().Add(3 * time.Hour).In(kyiv)
	if !strings.Contains(content, moved.Format("2006-01-02 15:04")) && !strings.Contains(content, moved.Add(-time.Minute).Format("2006-01-02 15:04")) {
		t.Fatalf("the model read %q, want about %s", content, moved.Format(time.DateTime))
	}
}

// A checkout under umask 002 leaves a skill's script writable by the group, and
// the node refuses such a script. The copy must not depend on how the repository
// was checked out.
func TestCopyDirIgnoresTheCheckoutsUmask(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	script, note := filepath.Join(src, "run.sh"), filepath.Join(src, "SKILL.md")
	for path, mode := range map[string]os.FileMode{script: 0o775, note: 0o664} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"run.sh": 0o755, "SKILL.md": 0o644} {
		st, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", name, st.Mode().Perm(), want)
		}
	}
}
