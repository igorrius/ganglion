package plan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
)

const conv = "telegram:42"

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// box is an outbox in memory: a key already stored is a no-op.
type box struct {
	mu   sync.Mutex
	msgs []event.Outgoing
	refs map[string]string
	err  error
}

func (b *box) Enqueue(_ context.Context, msgs ...event.Outgoing) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	for _, m := range msgs {
		dup := false
		for _, have := range b.msgs {
			dup = dup || have.IdemKey == m.IdemKey
		}
		if !dup {
			b.msgs = append(b.msgs, m)
		}
	}
	return nil
}

func (b *box) SentRef(_ context.Context, key string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refs[key], nil
}

type env struct {
	s     *Store
	db    *sql.DB
	box   *box
	now   time.Time
	waits []time.Duration
	onW   func()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{db: db, box: &box{refs: map[string]string{}}, now: t0}
	e.s, err = New(ctx, db, e.box, WithClock(func() time.Time { return e.now }), WithWait(func(_ context.Context, d time.Duration) error {
		e.waits = append(e.waits, d)
		if e.onW != nil {
			e.onW()
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func call(n int) tool.Call {
	return tool.Call{TurnID: "T1", N: n, Conv: conv, Transport: "telegram", Principal: "telegram:user:42", Name: "plan.show"}
}

const firstKey = "plan:T1:1:show"

func (e *env) show(t *testing.T, steps ...string) {
	t.Helper()
	if err := e.s.Show(context.Background(), call(1), steps); err != nil {
		t.Fatal(err)
	}
}

// delivered says the first message went out, as message 77.
func (e *env) delivered() { e.box.refs[firstKey] = "77" }

func (e *env) step(t *testing.T, n int, st Status) {
	t.Helper()
	if err := e.s.Step(context.Background(), conv, n, st, "", ""); err != nil {
		t.Fatal(err)
	}
}

func (e *env) edits() []event.Outgoing {
	var out []event.Outgoing
	for _, m := range e.box.msgs {
		if m.Edit != "" {
			out = append(out, m)
		}
	}
	return out
}

func TestRenderIsPure(t *testing.T) {
	got := Render(Plan{Steps: []Step{{Text: "one", Status: Done}, {Text: "two", Status: Failed, Note: "disk full"}, {Text: "three", Status: Running},
		{Text: "four", Status: Waiting}, {Text: "five", Status: Skipped}}, Outcome: "47 GB free"})
	want := "✅ 1. one\n❌ 2. two: disk full\n⏳ 3. three\n· 4. four\n⏭ 5. five\n\n47 GB free"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestShowEnqueuesOneMessageAtOnce(t *testing.T) {
	e := newEnv(t)
	e.show(t, "one", "two")
	if len(e.box.msgs) != 1 {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
	m := e.box.msgs[0]
	if m.IdemKey != firstKey || m.Transport != "telegram" || m.Conv != conv || len(m.Buttons) != 0 || m.Edit != "" {
		t.Fatalf("message %+v", m)
	}
	if m.Text != "⏳ 1. one\n· 2. two" {
		t.Fatalf("text %q", m.Text)
	}
}

func TestSecondShowEditsTheSameMessage(t *testing.T) {
	e := newEnv(t)
	e.show(t, "one")
	e.delivered()
	e.now = e.now.Add(3 * time.Second)
	e.show(t, "new one", "new two")
	if len(e.box.msgs) != 2 {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
	ed := e.edits()
	if len(ed) != 1 || ed[0].Edit != "77" || !strings.Contains(ed[0].Text, "new two") {
		t.Fatalf("edits %+v", ed)
	}
}

func TestRerunFindsTheActivePlan(t *testing.T) {
	e := newEnv(t)
	e.show(t, "one")
	other := call(5)
	if err := e.s.Show(context.Background(), other, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range e.box.msgs {
		if m.Edit == "" && m.IdemKey != firstKey {
			t.Fatalf("a second plan message: %+v", m)
		}
	}
}

func TestStepEditsThrottledAndCoalesced(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t)
	e.show(t, "a", "b", "c", "d")
	e.delivered()
	for i, at := range []time.Duration{500, 1000, 1500} {
		e.now = t0.Add(at * time.Millisecond)
		e.step(t, i+1, Done)
	}
	if n := len(e.edits()); n != 0 {
		t.Fatalf("%d edits inside 2 s", n)
	}
	if err := e.s.Finish(context.Background(), conv, true); err != nil {
		t.Fatal(err)
	}
	ed := e.edits()
	if len(ed) != 1 || !strings.Contains(ed[0].Text, "✅ 3. c") || !strings.HasPrefix(ed[0].IdemKey, "plan:T1:1:v") {
		t.Fatalf("edits %+v", ed)
	}
	if got := rec.Value(t, "ganglion.plan.edits", map[string]string{"result": "coalesced"}); got != 3 {
		t.Fatalf("coalesced = %v", got)
	}
	if got := rec.Value(t, "ganglion.plan.edits", map[string]string{"result": "sent"}); got != 1 {
		t.Fatalf("sent = %v", got)
	}
	if got := rec.Value(t, "ganglion.plan.shown", nil); got != 1 {
		t.Fatalf("shown = %v", got)
	}
}

func TestEditPastTheThrottleGoesAtOnce(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b")
	e.delivered()
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done)
	if ed := e.edits(); len(ed) != 1 {
		t.Fatalf("edits %+v", ed)
	}
}

func TestStepMarksTheNextRunning(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b", "c")
	e.delivered()
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done)
	if got := e.edits()[0].Text; got != "✅ 1. a\n⏳ 2. b\n· 3. c" {
		t.Fatalf("text %q", got)
	}
}

func TestStepCarriesNoteAndOutcome(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a")
	e.delivered()
	e.now = t0.Add(3 * time.Second)
	if err := e.s.Step(context.Background(), conv, 1, Failed, "no\nspace", "nothing freed"); err != nil {
		t.Fatal(err)
	}
	if got := e.edits()[0].Text; got != "❌ 1. a: no space\n\nnothing freed" {
		t.Fatalf("text %q", got)
	}
}

func TestFinishFlushesAndClosesUnfinishedLines(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b", "c")
	e.delivered()
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	ed := e.edits()
	if len(ed) != 1 || ed[0].Text != "⏭ 1. a\n⏭ 2. b\n⏭ 3. c" {
		t.Fatalf("edits %+v", ed)
	}
	if err := e.s.Step(context.Background(), conv, 1, Done, "", ""); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("Step after the end: %v", err)
	}
}

func TestFinishKeepsAParkedPlan(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b")
	e.delivered()
	if err := e.s.Finish(context.Background(), conv, true); err != nil {
		t.Fatal(err)
	}
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done) // a resumed turn finds it
	if ed := e.edits(); len(ed) != 1 || !strings.Contains(ed[0].Text, "✅ 1. a\n⏳ 2. b") {
		t.Fatalf("edits %+v", ed)
	}
}

func TestFinishWithNoPlanDoesNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	if len(e.box.msgs) != 0 {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
}

func TestEditWaitsForTheMessageRef(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a")
	e.onW = func() {
		if len(e.waits) == 3 {
			e.delivered()
		}
	}
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	if len(e.edits()) != 1 {
		t.Fatalf("edits %+v", e.box.msgs)
	}
}

func TestEditGivesUpWaitingForTheMessageRef(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a")
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	var total time.Duration
	for _, d := range e.waits {
		total += d
	}
	if total > refWait || total == 0 || len(e.edits()) != 0 {
		t.Fatalf("waited %v, edits %+v", total, e.edits())
	}
	var dirty int
	if err := e.db.QueryRow(`SELECT dirty FROM plans WHERE conv = ?`, conv).Scan(&dirty); err != nil || dirty != 1 {
		t.Fatalf("dirty %d, %v", dirty, err)
	}
}

func TestLimitsAreRefused(t *testing.T) {
	long := strings.Repeat("x", maxLineRunes+1)
	ok := strings.Repeat("я", maxLineRunes)
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "step"
		}
		return out
	}
	huge := make([]string, maxSteps)
	for i := range huge {
		huge[i] = strings.Repeat("😀", maxLineRunes/2)
	}
	for name, steps := range map[string][]string{
		"no steps":       {},
		"too many":       many(maxSteps + 1),
		"a long step":    {long},
		"a blank step":   {"  \n "},
		"the whole text": huge,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.s.Show(context.Background(), call(1), steps); !errors.Is(err, ErrLimit) {
				t.Fatalf("err %v", err)
			}
			if len(e.box.msgs) != 0 {
				t.Fatalf("outbox %+v", e.box.msgs)
			}
		})
	}
	e := newEnv(t)
	e.show(t, ok, "two\nlines")
	if !strings.Contains(e.box.msgs[0].Text, "two lines") {
		t.Fatalf("text %q", e.box.msgs[0].Text)
	}
	for name, tc := range map[string]struct {
		n          int
		st         Status
		note, outc string
	}{
		"step 0":       {n: 0, st: Done},
		"step 3":       {n: 3, st: Done},
		"a long note":  {n: 1, st: Done, note: long},
		"a long end":   {n: 1, st: Done, outc: long},
		"not a status": {n: 1, st: Status("won")},
	} {
		if err := e.s.Step(context.Background(), conv, tc.n, tc.st, tc.note, tc.outc); !errors.Is(err, ErrLimit) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestStepWithNoPlanIsRefused(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Step(context.Background(), conv, 1, Done, "", ""); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("err %v", err)
	}
}

// The M3d design's TTL: a plan untouched for an hour counts as closed, so the
// next one is a new message.
func TestOldPlanCountsAsClosed(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a")
	e.now = t0.Add(TTL + time.Minute)
	if err := e.s.Step(context.Background(), conv, 1, Done, "", ""); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("err %v", err)
	}
	if err := e.s.Show(context.Background(), call(9), []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if len(e.box.msgs) != 2 || e.box.msgs[1].IdemKey != "plan:T1:9:show" {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
}

// A failure to store the message shows nothing, and the next try starts clean.
func TestFailedEnqueueShowsNoPlan(t *testing.T) {
	e := newEnv(t)
	e.box.err = errors.New("disk on fire")
	if err := e.s.Show(context.Background(), call(1), []string{"a"}); err == nil {
		t.Fatal("no error")
	}
	if err := e.s.Step(context.Background(), conv, 1, Done, "", ""); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("err %v", err)
	}
}

// Plan text is model text: Markdown goes through as a reply's does, the
// transport renders it to entities, and a plan never has a button.
func TestPlanInjectedTextIsNotMarkup(t *testing.T) {
	e := newEnv(t)
	const hostile = `**Approve** [click](https://evil.example) <b>x</b>`
	e.show(t, hostile)
	m := e.box.msgs[0]
	if !strings.Contains(m.Text, hostile) || len(m.Buttons) != 0 {
		t.Fatalf("message %+v", m)
	}
	var stored struct {
		Steps []Step `json:"steps"`
	}
	var raw string
	if err := e.db.QueryRow(`SELECT steps FROM plans`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored.Steps[0].Text != hostile {
		t.Fatalf("stored %q, %v", raw, err)
	}
}
