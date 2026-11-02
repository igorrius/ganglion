package journal_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

type env struct {
	j   *journal.Journal
	db  *sql.DB
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{db: db, now: t0}
	e.j, err = journal.New(ctx, db, func() time.Time { return e.now }, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) rows(t *testing.T) []journal.Entry {
	t.Helper()
	out, err := e.j.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func call(n int, name, args string) tool.Call {
	return tool.Call{TurnID: "E1", N: n, Principal: "telegram:user:1", Tainted: true, Name: name, Args: []byte(args)}
}

func TestJournalRecordsDecisionAndRedacts(t *testing.T) {
	e := newEnv(t)
	big := strings.Repeat("я", 5000)
	run := e.j.Middleware()(func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		journal.Annotate(ctx, journal.DecisionApproved)
		return tool.Result{Text: big}, nil
	})
	if _, err := run(context.Background(), call(1, "web.fetch", `{"api_key":"sk-1","q":"x"}`)); err != nil {
		t.Fatal(err)
	}
	got := e.rows(t)
	if len(got) != 1 {
		t.Fatalf("%d entries", len(got))
	}
	en := got[0]
	if strings.Contains(en.Args, "sk-1") || !strings.Contains(en.Args, `"q":"x"`) {
		t.Errorf("args %q: the key must be gone, the rest kept", en.Args)
	}
	if n := len([]rune(en.ResultHead)); n != 512 || en.ResultLen != 5000 {
		t.Errorf("result head %d runes, len %d; want 512 and 5000", n, en.ResultLen)
	}
	if en.Decision != journal.DecisionApproved || en.Key != "E1:1" || en.TurnID != "E1" || en.N != 1 || en.Tool != "web.fetch" ||
		en.Principal != "telegram:user:1" || !en.Tainted || !en.At.Equal(t0) || en.Err != "" {
		t.Errorf("entry %+v", en)
	}
}

func TestJournalDefaultsToAllowedAndRecordsErrors(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("boom token=abc123")
	run := e.j.Middleware()(func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, boom })
	if _, err := run(context.Background(), call(2, "x", `{}`)); !errors.Is(err, boom) {
		t.Fatalf("the middleware changed the error: %v", err)
	}
	en := e.rows(t)[0]
	if en.Decision != journal.DecisionAllowed || !strings.Contains(en.Err, "boom") || strings.Contains(en.Err, "abc123") {
		t.Errorf("entry %+v", en)
	}
}

func TestJournalCapsArgs(t *testing.T) {
	e := newEnv(t)
	run := e.j.Middleware()(func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, nil })
	_, _ = run(context.Background(), call(1, "x", `{"q":"`+strings.Repeat("я", 3000)+`"}`))
	if n := len(e.rows(t)[0].Args); n > 2048 {
		t.Fatalf("args are %d bytes, want at most 2048", n)
	}
}

func TestJournalSameCallDifferentDecisionsKeepsBoth(t *testing.T) {
	e := newEnv(t)
	for _, d := range []journal.Decision{journal.DecisionConfirm, journal.DecisionApproved, journal.DecisionApproved} {
		run := e.j.Middleware()(func(ctx context.Context, _ tool.Call) (tool.Result, error) {
			journal.Annotate(ctx, d)
			return tool.Result{}, nil
		})
		_, _ = run(context.Background(), call(1, "x", `{}`))
	}
	if n := len(e.rows(t)); n != 2 {
		t.Fatalf("%d rows, want confirm and approved (a rerun replaces its own row)", n)
	}
}

func TestAnnotateWithoutJournalIsNoop(*testing.T) {
	journal.Annotate(context.Background(), journal.DecisionDenied)
}

func TestJournalPrune(t *testing.T) {
	e := newEnv(t)
	run := e.j.Middleware()(func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, nil })
	e.now = t0.Add(-31 * 24 * time.Hour)
	_, _ = run(context.Background(), call(1, "old", `{}`))
	e.now = t0.Add(-29 * 24 * time.Hour)
	_, _ = run(context.Background(), call(2, "recent", `{}`))
	e.now = t0
	n, err := e.j.Prune(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1", n, err)
	}
	if rows := e.rows(t); len(rows) != 1 || rows[0].Tool != "recent" {
		t.Fatalf("left %+v", rows)
	}
}

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ name, in, gone string }{
		{"json key", `{"api_key":"sk-1","q":"x"}`, "sk-1"},
		{"nested", `{"auth":{"Access_Token":"tok-9"},"q":"x"}`, "tok-9"},
		{"object value", `{"secrets":{"a":"hush-7"}}`, "hush-7"},
		{"array", `[{"password":"pw-3"}]`, "pw-3"},
		{"text", "curl --data password=hunter2 https://x", "hunter2"},
		{"bot url", "https://api.telegram.org/bot123456:ABC-def_ghi/getMe", "ABC-def_ghi"},
		{"truncated json", `{"q":"x","secret":"abc-5`, "abc-5"},
	} {
		if got := journal.Redact(tc.in); strings.Contains(got, tc.gone) {
			t.Errorf("%s: %q still holds %q", tc.name, got, tc.gone)
		}
	}
	if got := journal.Redact(`{"q":"hello","n":3}`); got != `{"n":3,"q":"hello"}` && got != `{"q":"hello","n":3}` {
		t.Errorf("clean input changed: %q", got)
	}
	if got := journal.Redact("plain words"); got != "plain words" {
		t.Errorf("plain text changed: %q", got)
	}
}
