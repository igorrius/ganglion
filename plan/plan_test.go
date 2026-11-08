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
	"github.com/igorrius/ganglion/tool/pending"
)

const conv = "telegram:42"

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// box is an outbox in memory: a key already stored is a no-op.
type box struct {
	mu   sync.Mutex
	msgs []event.Outgoing
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

type env struct {
	s   *Store
	db  *sql.DB
	box *box
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{db: db, box: &box{}, now: t0}
	e.s, err = New(ctx, db, e.box, WithClock(func() time.Time { return e.now }))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func call(n int) tool.Call {
	return tool.Call{TurnID: "T1", N: n, Conv: conv, Transport: "telegram", Principal: "telegram:user:42", Name: "plan.show"}
}

const firstKey = "plan:T1:1:show"

// words are steps of words only.
func words(texts ...string) []Step {
	out := make([]Step, len(texts))
	for i, s := range texts {
		out[i] = Step{Text: s}
	}
	return out
}

func (e *env) show(t *testing.T, steps ...string) {
	t.Helper()
	if err := e.s.Show(context.Background(), call(1), words(steps...)); err != nil {
		t.Fatal(err)
	}
}

func (e *env) step(t *testing.T, n int, st Status) {
	t.Helper()
	if err := e.s.Step(context.Background(), conv, n, st, "", ""); err != nil {
		t.Fatal(err)
	}
}

func (e *env) edits() []event.Outgoing {
	var out []event.Outgoing
	for _, m := range e.box.msgs {
		if m.EditOf != "" {
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
	if m.IdemKey != firstKey || m.Transport != "telegram" || m.Conv != conv || len(m.Buttons) != 0 || m.EditOf != "" {
		t.Fatalf("message %+v", m)
	}
	if m.Text != "⏳ 1. one\n· 2. two" {
		t.Fatalf("text %q", m.Text)
	}
}

func TestSecondShowEditsTheSameMessage(t *testing.T) {
	e := newEnv(t)
	e.show(t, "one")
	e.now = e.now.Add(3 * time.Second)
	e.show(t, "new one", "new two")
	if len(e.box.msgs) != 2 {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
	ed := e.edits()
	if len(ed) != 1 || ed[0].EditOf != firstKey || !strings.Contains(ed[0].Text, "new two") {
		t.Fatalf("edits %+v", ed)
	}
}

func TestRerunFindsTheActivePlan(t *testing.T) {
	e := newEnv(t)
	e.show(t, "one")
	other := call(5)
	if err := e.s.Show(context.Background(), other, words("one")); err != nil {
		t.Fatal(err)
	}
	for _, m := range e.box.msgs {
		if m.EditOf == "" && m.IdemKey != firstKey {
			t.Fatalf("a second plan message: %+v", m)
		}
	}
}

func TestStepEditsThrottledAndCoalesced(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t)
	e.show(t, "a", "b", "c", "d")
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
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done)
	if ed := e.edits(); len(ed) != 1 {
		t.Fatalf("edits %+v", ed)
	}
}

func TestStepMarksTheNextRunning(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b", "c")
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done)
	if got := e.edits()[0].Text; got != "✅ 1. a\n⏳ 2. b\n· 3. c" {
		t.Fatalf("text %q", got)
	}
}

func TestStepCarriesNoteAndOutcome(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a")
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

// Review: the end of a turn does not wait for the first message: the sender
// finds it when the edit's turn comes, so the plan closes under the answer even
// when delivery is held up.
func TestFinishClosesAPlanNotDeliveredYet(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b")
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	ed := e.edits()
	if len(ed) != 1 || ed[0].EditOf != firstKey || ed[0].Text != "⏭ 1. a\n⏭ 2. b" {
		t.Fatalf("edits %+v", ed)
	}
	var active, dirty int
	if err := e.db.QueryRow(`SELECT active, dirty FROM plans WHERE conv = ?`, conv).Scan(&active, &dirty); err != nil || active != 0 || dirty != 0 {
		t.Fatalf("active %d, dirty %d, %v", active, dirty, err)
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
			if err := e.s.Show(context.Background(), call(1), words(steps...)); !errors.Is(err, ErrLimit) {
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
	if err := e.s.Show(context.Background(), call(9), words("b")); err != nil {
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
	if err := e.s.Show(context.Background(), call(1), words("a")); err == nil {
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

// A live measurement: the model did the work, ticked the first step and the last,
// and the lines between read as skipped. A later step that ended means the
// earlier ones are behind it, unless the model said otherwise.
func TestLaterStepEndingClosesTheEarlierOnes(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b", "c", "d")
	ctx := context.Background()
	if err := e.s.Step(ctx, conv, 2, Failed, "no page", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Step(ctx, conv, 3, Skipped, "", ""); err != nil {
		t.Fatal(err)
	}
	e.now = t0.Add(3 * time.Second)
	if err := e.s.Step(ctx, conv, 4, Done, "", "all done"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Finish(ctx, conv, false); err != nil {
		t.Fatal(err)
	}
	ed := e.edits()
	want := "✅ 1. a\n❌ 2. b: no page\n⏭ 3. c\n✅ 4. d\n\nall done"
	if got := ed[len(ed)-1].Text; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A line never reached when the turn ends is what ⏭ is for.
func TestLinesNeverReachedStaySkipped(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b", "c")
	e.step(t, 1, Done)
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	ed := e.edits()
	if got := ed[len(ed)-1].Text; got != "✅ 1. a\n⏭ 2. b\n⏭ 3. c" {
		t.Fatalf("got %q", got)
	}
}

// last is the latest message stored.
func (b *box) last() event.Outgoing {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) == 0 {
		return event.Outgoing{}
	}
	return b.msgs[len(b.msgs)-1]
}

// approve shows a plan in conv through a pressed question, one step per
// command, as the approved run of plan.show does.
func (e *env) approve(t *testing.T, conv string, cmds ...tool.Command) tool.Call {
	t.Helper()
	c := tool.Call{TurnID: "A" + conv, N: 1, Conv: conv, Transport: "telegram", Approved: true}
	steps := make([]Step, len(cmds))
	for i := range cmds {
		steps[i] = Step{Text: "step", Command: &cmds[i]}
	}
	if err := e.s.Show(context.Background(), c, steps); err != nil {
		t.Fatal(err)
	}
	return c
}

func argv(a ...string) tool.Command { return tool.Command{Argv: a} }

func (e *env) approved(t *testing.T, conv string) []tool.Command {
	t.Helper()
	got, err := e.s.Approved(context.Background(), conv)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// R-CMD-7, ruling 8: the pressed question becomes the plan, edited in place
// with no buttons, and only the commands that need a press are approved.
func TestAnApprovedPlanTakesTheQuestionAsItsMessage(t *testing.T) {
	e := newEnv(t)
	c := tool.Call{TurnID: "e1", N: 1, Conv: conv, Transport: "telegram", Approved: true}
	steps := []Step{
		{Text: "Measure"},
		{Text: "Clean Go's cache", Command: &tool.Command{Argv: []string{"go", "clean", "-cache"}}},
		{Text: "Look again", Command: &tool.Command{Argv: []string{"df", "-h"}}},
	}
	if err := e.s.Show(context.Background(), c, steps); err != nil {
		t.Fatal(err)
	}
	m := e.box.last()
	if m.EditOf != "e1:1"+pending.QuestionKeySuffix || len(m.Buttons) != 0 || !strings.Contains(m.Text, "`go clean -cache`") {
		t.Fatalf("message = %+v; want an edit of the question with the plan and no buttons", m)
	}
	if len(e.box.msgs) != 1 {
		t.Fatalf("outbox %+v; want only the edit of the question", e.box.msgs)
	}
	got := e.approved(t, conv)
	if len(got) != 1 || !got[0].Equal(argv("go", "clean", "-cache")) {
		t.Fatalf("approved = %+v; want only the command that needs a press", got)
	}
	// The plan's later edits change the question too.
	e.now = t0.Add(3 * time.Second)
	e.step(t, 1, Done)
	if ed := e.box.last(); ed.EditOf != "e1:1"+pending.QuestionKeySuffix || !strings.Contains(ed.Text, "✅ 1. Measure") {
		t.Fatalf("step edit %+v", ed)
	}
}

// A command step shows its exact command under the words, in a code span
// a backtick cannot end, and its folder.
func TestRenderShowsACommandUnderItsStep(t *testing.T) {
	e := newEnv(t)
	steps := []Step{
		{Text: "Clean", Command: &tool.Command{Argv: []string{"rm", "-r", "build"}, Dir: "/srv/app"}},
		{Text: "Odd", Command: &tool.Command{Argv: []string{"echo", "a`b"}}},
	}
	if err := e.s.Show(context.Background(), call(1), steps); err != nil {
		t.Fatal(err)
	}
	want := "⏳ 1. Clean\n    `rm -r build` in `/srv/app`\n· 2. Odd\n    `` echo 'a`b' ``"
	if got := e.box.msgs[0].Text; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// Review focus 6: a new plan shown without a press approves nothing new; it
// keeps only the approved commands it still lists.
func TestAnUnapprovedPlanApprovesNothingNew(t *testing.T) {
	e := newEnv(t)
	e.approve(t, conv, argv("go", "clean", "-cache"), argv("rm", "-r", "/tmp/a"))
	c := tool.Call{TurnID: "e2", N: 1, Conv: conv, Transport: "telegram"}
	steps := []Step{
		{Text: "x", Command: &tool.Command{Argv: []string{"go", "clean", "-cache"}}},
		{Text: "y", Command: &tool.Command{Argv: []string{"cargo", "clean"}}},
		{Text: "z", Command: &tool.Command{Argv: []string{"rm", "-r", "/tmp/a"}, Dir: "/elsewhere"}},
	}
	if err := e.s.Show(context.Background(), c, steps); err != nil {
		t.Fatal(err)
	}
	got := e.approved(t, conv)
	if len(got) != 1 || !got[0].Equal(argv("go", "clean", "-cache")) {
		t.Fatalf("approved = %+v; want the one the new plan still lists, in its folder", got)
	}
	// The change edits the question the plan became, not a new message.
	if m := e.box.last(); m.EditOf != "A"+conv+":1"+pending.QuestionKeySuffix {
		t.Fatalf("message %+v", m)
	}
}

func TestApprovalEndsWithThePlan(t *testing.T) {
	e := newEnv(t)
	e.approve(t, conv, argv("go", "clean", "-cache"))
	if err := e.s.Finish(context.Background(), conv, false); err != nil {
		t.Fatal(err)
	}
	if got := e.approved(t, conv); len(got) != 0 {
		t.Fatalf("a closed plan still approves %+v", got)
	}
}

// The approval's hour runs from the press, not from the plan's last change:
// a plan kept open by its steps (or by a command in the background) does not
// stretch it.
func TestApprovalEndsAfterAnHourEvenWhileThePlanMoves(t *testing.T) {
	e := newEnv(t)
	e.approve(t, conv, argv("go", "clean", "-cache"), argv("cargo", "clean"))
	e.now = t0.Add(50 * time.Minute)
	e.step(t, 1, Done)
	if got := e.approved(t, conv); len(got) != 2 {
		t.Fatalf("approved at 50 min = %+v", got)
	}
	e.now = t0.Add(61 * time.Minute)
	if got := e.approved(t, conv); len(got) != 0 {
		t.Fatalf("approved past the hour = %+v", got)
	}
	e.step(t, 2, Done) // the plan itself is still shown and moving
	// A plan shown again without a press does not renew the hour.
	c := tool.Call{TurnID: "e3", N: 1, Conv: conv, Transport: "telegram"}
	if err := e.s.Show(context.Background(), c, []Step{{Text: "x", Command: &tool.Command{Argv: []string{"cargo", "clean"}}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.approved(t, conv); len(got) != 0 {
		t.Fatalf("approved after a new plan past the hour = %+v", got)
	}
}

func TestApprovalIsTheConversations(t *testing.T) {
	e := newEnv(t)
	e.approve(t, "telegram:1", argv("go", "clean", "-cache"))
	if got := e.approved(t, "telegram:2"); len(got) != 0 {
		t.Fatalf("another conversation inherits %+v", got)
	}
	if got := e.approved(t, "telegram:1"); len(got) != 1 {
		t.Fatalf("the conversation's own approval = %+v", got)
	}
}

// A plan shown before the press is closed under its own message; the
// approved one is the question.
func TestAnApprovedPlanClosesTheOneBefore(t *testing.T) {
	e := newEnv(t)
	e.show(t, "a", "b")
	e.approve(t, conv, argv("go", "clean", "-cache"))
	var closed, approved bool
	for _, m := range e.edits() {
		closed = closed || m.EditOf == firstKey && m.Text == "⏭ 1. a\n⏭ 2. b"
		approved = approved || m.EditOf == "A"+conv+":1"+pending.QuestionKeySuffix
	}
	if !closed || !approved {
		t.Fatalf("edits %+v", e.edits())
	}
}

// A rerun of the approved plan.show (a crash before its outcome was stored)
// stores the same edit once and keeps the approval.
func TestRerunOfAnApprovedPlanEditsOnce(t *testing.T) {
	e := newEnv(t)
	e.approve(t, conv, argv("go", "clean", "-cache"))
	e.approve(t, conv, argv("go", "clean", "-cache"))
	if len(e.box.msgs) != 1 {
		t.Fatalf("outbox %+v", e.box.msgs)
	}
	if got := e.approved(t, conv); len(got) != 1 {
		t.Fatalf("approved %+v", got)
	}
}

// A conversation with no plan approves nothing, and a plan of words only
// approves nothing either.
func TestNoPlanApprovesNothing(t *testing.T) {
	e := newEnv(t)
	if got := e.approved(t, conv); len(got) != 0 {
		t.Fatalf("approved %+v", got)
	}
	e.show(t, "a")
	if got := e.approved(t, conv); len(got) != 0 {
		t.Fatalf("approved %+v", got)
	}
}

// A command line counts within the plan's limit, so the plan still fits one
// message.
func TestACommandCountsWithinTheLimit(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x", 1000)
	steps := []Step{
		{Text: "a", Command: &tool.Command{Argv: []string{"echo", long}}},
		{Text: "b", Command: &tool.Command{Argv: []string{"echo", long}}},
		{Text: "c", Command: &tool.Command{Argv: []string{"echo", long}}},
		{Text: "d", Command: &tool.Command{Argv: []string{"echo", long}}},
	}
	if err := e.s.Show(context.Background(), call(1), steps); !errors.Is(err, ErrLimit) {
		t.Fatalf("err %v", err)
	}
}
