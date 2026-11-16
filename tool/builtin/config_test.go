package builtin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/pending"
)

// fakeHistory records what was recorded and serves canned changes.
type fakeHistory struct {
	records  []recorded
	changes  []confighist.Change
	versions map[string][2]string
	none     bool
}

type recorded struct {
	src    confighist.Source
	reason string
}

func (h *fakeHistory) Record(_ context.Context, src confighist.Source, reason string, write func() error) error {
	if err := write(); err != nil {
		return err
	}
	h.records = append(h.records, recorded{src, reason})
	return nil
}

func (h *fakeHistory) Sync(context.Context) error { return nil }

func (h *fakeHistory) Changes(_ context.Context, n int) ([]confighist.Change, error) {
	if h.none {
		return nil, confighist.ErrNoHistory
	}
	return h.changes[:min(n, len(h.changes))], nil
}

func (h *fakeHistory) Diff(_ context.Context, id string) (string, error) {
	v := h.versions[id]
	return "-" + v[0] + "+" + v[1], nil
}

func (h *fakeHistory) Versions(_ context.Context, id string) ([]byte, []byte, error) {
	v, ok := h.versions[id]
	if !ok {
		return nil, nil, confighist.ErrNoChange
	}
	return []byte(v[0]), []byte(v[1]), nil
}

type configButtons struct{ added []pending.Button }

func (b *configButtons) AddButtons(_ context.Context, bs ...pending.Button) error {
	b.added = append(b.added, bs...)
	return nil
}

type configEnv struct {
	g        builtin.Configuring
	h        *fakeHistory
	b        *configButtons
	restarts int
}

func newConfig(t *testing.T, content string) *configEnv {
	t.Helper()
	file := filepath.Join(t.TempDir(), settings.FileName)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &configEnv{h: &fakeHistory{versions: map[string][2]string{}}, b: &configButtons{}}
	e.g = builtin.Configuring{File: file, History: e.h, Buttons: e.b, Restart: func() { e.restarts++ },
		Now: func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }}
	return e
}

func (e *configEnv) tool(t *testing.T, name string) tool.Handler {
	t.Helper()
	for _, h := range e.g.Tools() {
		if h.Spec().Name == name {
			return h
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func (e *configEnv) question(t *testing.T, name string, c tool.Call) (string, error) {
	t.Helper()
	return e.tool(t, name).(tool.Questioner).Question(context.Background(), c)
}

func (e *configEnv) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.g.File)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const turns200 = "agent:\n  limits:\n    daily_turns: 200\n"

func TestProposeRefusesAFieldOffTheList(t *testing.T) {
	e := newConfig(t, "")
	for path, place := range map[string]string{"sandbox.deny": "host", "telegram.admin": "host", "telegram.groups": "own tool"} {
		_, err := e.question(t, builtin.ConfigProposeTool, call(builtin.ConfigProposeTool,
			`{"changes":[{"path":"`+path+`","value":[]}],"reason":"x"}`))
		if !errors.Is(err, settings.ErrNotAllowed) || !strings.Contains(err.Error(), strings.Fields(place)[0]) {
			t.Errorf("%s: err = %v, want ErrNotAllowed naming the %s", path, err, place)
		}
	}
}

func TestProposeRefusesABadValueBeforeTheCard(t *testing.T) {
	e := newConfig(t, "")
	_, err := e.question(t, builtin.ConfigProposeTool, call(builtin.ConfigProposeTool,
		`{"changes":[{"path":"agent.limits.daily_turns","value":"many"}],"reason":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_turns") {
		t.Fatalf("err = %v", err)
	}
}

func TestProposeRefusesBadArguments(t *testing.T) {
	e := newConfig(t, "")
	for _, args := range []string{
		`{"changes":[],"reason":"x"}`,
		`{"changes":[{"path":"log.level","value":"debug"}],"reason":" "}`,
		`{"changes":[{"path":"log.level","value":"debug"},{"path":"log.level","value":"info"}],"reason":"x"}`,
		`{"changes":[{"path":"log.level","value":"debug"}],"reason":"x","more":1}`,
	} {
		if _, err := e.question(t, builtin.ConfigProposeTool, call(builtin.ConfigProposeTool, args)); err == nil {
			t.Errorf("%s: no refusal", args)
		}
	}
}

func TestProposeQuestionShowsOldAndNewAndTaint(t *testing.T) {
	e := newConfig(t, turns200)
	c := call(builtin.ConfigProposeTool, `{"changes":[{"path":"agent.limits.daily_turns","value":300},{"path":"log.level","value":"debug"}],"reason":"more room"}`)
	q, err := e.question(t, builtin.ConfigProposeTool, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"daily\\_turns: 200 → 300", "log.level: default → debug", "more room"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "outside") {
		t.Errorf("a clean session's question speaks of outside content:\n%s", q)
	}
	c.Tainted = true
	if q, _ = e.question(t, builtin.ConfigProposeTool, c); !strings.Contains(q, "from outside") {
		t.Errorf("a tainted session's question does not say so:\n%s", q)
	}
}

func TestProposeRunWritesThroughTheHistoryAndOffersRestart(t *testing.T) {
	e := newConfig(t, turns200)
	c := call(builtin.ConfigProposeTool, `{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"more room"}`)
	c.Approved = true
	res, err := e.tool(t, builtin.ConfigProposeTool).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.h.records) != 1 || e.h.records[0] != (recorded{confighist.SourcePropose, "more room"}) {
		t.Fatalf("records = %+v", e.h.records)
	}
	if !strings.Contains(e.file(t), "daily_turns: 300") {
		t.Fatalf("not written:\n%s", e.file(t))
	}
	if !strings.Contains(res.Text, "agent.limits.daily_turns: 200 -> 300") {
		t.Fatalf("result = %q", res.Text)
	}
	if len(res.Out) != 1 || len(res.Out[0].Buttons) != 1 || len(e.b.added) != 1 ||
		e.b.added[0].Action != builtin.ActionConfigRestart || e.b.added[0].ID != res.Out[0].Buttons[0].ID ||
		e.b.added[0].Principal != c.Principal || e.b.added[0].Conv != c.Conv {
		t.Fatalf("no restart button: %+v, %+v", res.Out, e.b.added)
	}
}

func TestProposeRunChecksAgainstTheFileAsItIsNow(t *testing.T) {
	e := newConfig(t, "")
	c := call(builtin.ConfigProposeTool, `{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"x"}`)
	if _, err := e.question(t, builtin.ConfigProposeTool, c); err != nil {
		t.Fatal(err)
	}
	// While the card waits, the file is broken on the host.
	if err := os.WriteFile(e.g.File, []byte("agent: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Approved = true
	if _, err := e.tool(t, builtin.ConfigProposeTool).Run(context.Background(), c); err == nil {
		t.Fatal("written into a file the start would refuse")
	}
	if len(e.h.records) != 0 {
		t.Fatal("recorded")
	}
}

func TestRollbackProposesTheEarlierValuesOfAllowedFieldsOnly(t *testing.T) {
	const before = "agent:\n  limits:\n    daily_turns: 200\nsandbox:\n  hide_names: true\n"
	const after = "agent:\n  limits:\n    daily_turns: 300\n  call_timeout: 2m\nsandbox:\n  hide_names: false\n"
	e := newConfig(t, after)
	e.h.changes = []confighist.Change{{ID: "abcdef1234567890", Source: confighist.SourceHost, Reason: "edited"}}
	e.h.versions["abcdef1234567890"] = [2]string{before, after}
	c := call(builtin.ConfigRollbackTool, `{}`)
	q, err := e.question(t, builtin.ConfigRollbackTool, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"daily\\_turns: 300 → 200", "call\\_timeout: 2m → default", "sandbox.hide\\_names (host)", "roll back abcdef12"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q:\n%s", want, q)
		}
	}
	c.Approved = true
	if _, err := e.tool(t, builtin.ConfigRollbackTool).Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got := e.file(t)
	if !strings.Contains(got, "daily_turns: 200") || strings.Contains(got, "call_timeout") || !strings.Contains(got, "hide_names: false") {
		t.Fatalf("file after the rollback:\n%s", got)
	}
	if e.h.records[0].src != confighist.SourceRollback {
		t.Fatalf("records = %+v", e.h.records)
	}
}

func TestRollbackSkipsChangesOfOtherFields(t *testing.T) {
	e := newConfig(t, turns200)
	e.h.changes = []confighist.Change{
		{ID: "1111111111", Source: confighist.SourceModule, Reason: "module x: settings"},
		{ID: "2222222222", Source: confighist.SourcePropose, Reason: "more room"},
	}
	e.h.versions["1111111111"] = [2]string{turns200, turns200 + "modules:\n  - name: local/x\n"}
	e.h.versions["2222222222"] = [2]string{"", turns200}
	q, err := e.question(t, builtin.ConfigRollbackTool, call(builtin.ConfigRollbackTool, `{}`))
	if err != nil || !strings.Contains(q, "roll back 22222222") {
		t.Fatalf("question = %q, %v", q, err)
	}
	_, err = e.question(t, builtin.ConfigRollbackTool, call(builtin.ConfigRollbackTool, `{"change":"1111111111"}`))
	if err == nil || !strings.Contains(err.Error(), "modules") {
		t.Fatalf("a module's change: err = %v", err)
	}
	if _, err = e.question(t, builtin.ConfigRollbackTool, call(builtin.ConfigRollbackTool, `{"change":"99999999"}`)); !errors.Is(err, confighist.ErrNoChange) {
		t.Fatalf("an unknown change: err = %v", err)
	}
}

func TestHistoryWithoutGitSaysSo(t *testing.T) {
	e := newConfig(t, "")
	e.h.none = true
	res, err := e.tool(t, builtin.ConfigHistoryTool).Run(context.Background(), call(builtin.ConfigHistoryTool, `{}`))
	if err != nil || !strings.Contains(res.Text, "no history") {
		t.Fatalf("history = %q, %v", res.Text, err)
	}
}

func TestHistoryListsChanges(t *testing.T) {
	e := newConfig(t, "")
	e.h.changes = []confighist.Change{{ID: "abcdef1234567890", When: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
		Source: confighist.SourcePropose, Reason: "more room"}}
	e.h.versions["abcdef1234567890"] = [2]string{"a", "b"}
	res, err := e.tool(t, builtin.ConfigHistoryTool).Run(context.Background(), call(builtin.ConfigHistoryTool, `{"limit":5}`))
	if err != nil || !strings.Contains(res.Text, "abcdef12 2026-11-16 10:00 propose: more room") || res.External {
		t.Fatalf("history = %q, %v", res.Text, err)
	}
}

func TestRestartButtonRestarts(t *testing.T) {
	e := newConfig(t, "")
	edit, err := e.g.QuickActions()[builtin.ActionConfigRestart](context.Background(), pending.QuickPress{Speak: texts.Speaker{}})
	if err != nil || e.restarts != 1 || !strings.Contains(edit.Text, "Restarting") {
		t.Fatalf("edit = %+v, %v, restarts %d", edit, err, e.restarts)
	}
}

func TestConfigToolsAreAdminOnlyAndNotUnderAGrant(t *testing.T) {
	e := newConfig(t, "")
	for _, h := range e.g.Tools() {
		s := h.Spec()
		if !s.AdminOnly {
			t.Errorf("%s is not admin only", s.Name)
		}
		c := call(s.Name, `{}`)
		c.Grant = &tool.Grant{}
		if _, err := h.Run(context.Background(), c); err == nil {
			t.Errorf("%s ran under a grant", s.Name)
		}
		if q, ok := h.(tool.Questioner); ok && s.Policy == tool.PolicyConfirm {
			if _, err := q.Question(context.Background(), c); err == nil {
				t.Errorf("%s asked under a grant", s.Name)
			}
		}
	}
}
