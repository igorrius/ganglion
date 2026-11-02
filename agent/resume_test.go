package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
)

const owner = "telegram:user:42"

// sideTool is a side-effect tool: the policy asks before it runs.
type sideTool struct {
	fakeTool
	class tool.Class
}

func (s *sideTool) Spec() tool.Spec {
	sp := s.fakeTool.Spec()
	sp.Class, sp.Policy = s.class, tool.PolicyConfirm
	return sp
}

func newSideTool() *sideTool {
	return &sideTool{name: "schedule.create", result: tool.Result{Text: "created"}, class: tool.ClassSideEffect}
}

// confirmEnv is an env with the production chain around tools: the
// journal, then the real policy asking through the pending store.
type confirmEnv struct {
	*env
	journal *journal.Journal
}

func newConfirmEnv(t *testing.T, hs ...tool.Handler) *confirmEnv {
	t.Helper()
	reg := registry(t, hs...)
	e := newEnv(t, Config{}, reg)
	j, err := journal.New(context.Background(), e.db, func() time.Time { return e.now }, journal.DefaultRetain)
	if err != nil {
		t.Fatal(err)
	}
	e.deps.Middleware = []tool.Middleware{j.Middleware()}
	e.deps.Policy = policy.New(policy.DefaultConfig(), reg, e.pending).Middleware()
	e.a = New(overDefaults(Config{}), e.deps)
	return &confirmEnv{env: e, journal: j}
}

// press is a decision event: from pressed button in the message ref.
func press(from, button, ref string) event.Event {
	seq++
	p, _ := json.Marshal(event.Decision{Button: button, From: from, Name: "Igor", Ref: ref})
	return event.Event{ID: fmt.Sprintf("01PR%04d", seq), IdemKey: fmt.Sprintf("telegram:7001:%d", seq), Source: "telegram",
		Conv: "telegram:42", Kind: event.KindDecision, Payload: p, At: t0}
}

// ask runs a turn in which the model calls the tool and then says it
// asked; it returns the question sent and the turn's event.
func (e *confirmEnv) ask(t *testing.T, name string) (event.Outgoing, event.Event) {
	t.Helper()
	e.smart.push(callTool(name), say("I asked the owner."))
	ev := message(owner, "Igor", "do it")
	out := e.turn(t, ev)
	for _, o := range out {
		if len(o.Buttons) > 0 {
			return o, ev
		}
	}
	t.Fatalf("no question in %+v", out)
	return event.Outgoing{}, ev
}

func (e *confirmEnv) decisions(t *testing.T, key string) []journal.Decision {
	t.Helper()
	entries, err := e.journal.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.Decision
	for _, en := range entries {
		if en.Key == key {
			out = append(out, en.Decision)
		}
	}
	return out
}

func lastUser(req provider.ChatRequest) string {
	text, _, _ := strings.Cut(lastUserRaw(req), "\n[now: ")
	return text
}

// lastUserRaw is the last user message as the model read it, with the
// lines the turn appended.
func lastUserRaw(req provider.ChatRequest) string {
	for _, m := range slices.Backward(req.Messages) {
		if m.Role == provider.RoleUser {
			return m.Content
		}
	}
	return ""
}

func findOut(out []event.Outgoing, pred func(event.Outgoing) bool) (event.Outgoing, bool) {
	for _, o := range out {
		if pred(o) {
			return o, true
		}
	}
	return event.Outgoing{}, false
}

func isEdit(o event.Outgoing) bool  { return o.Edit != "" }
func isReply(o event.Outgoing) bool { return o.Edit == "" && len(o.Buttons) == 0 }

func TestConfirmThenApproveResumes(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, ev := e.ask(t, "schedule.create")
	if len(side.calls) != 0 {
		t.Fatal("the tool ran before the owner answered")
	}
	if q.IdemKey != ev.ID+":1:confirm" || q.Transport != "telegram" || q.Conv != "telegram:42" || len(q.Buttons) != 2 ||
		!strings.Contains(q.Text, "schedule.create") {
		t.Fatalf("question %+v", q)
	}
	st := e.state(t)
	if len(st.Pending) != 1 {
		t.Fatalf("pending %v", st.Pending)
	}
	e.smart.push(say("Done: created."))
	out := e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	if len(side.calls) != 1 || side.calls[0].Key() != ev.ID+":1" || !side.calls[0].Admin || side.calls[0].Transport != "telegram" {
		t.Fatalf("calls %+v", side.calls)
	}
	edit, ok := findOut(out, isEdit)
	if !ok || edit.Edit != "77" || !strings.HasPrefix(edit.Text, q.Text) || !strings.HasSuffix(edit.Text, "Approved.") || len(edit.Buttons) != 0 {
		t.Fatalf("edit %+v in %+v", edit, out)
	}
	reply, ok := findOut(out, isReply)
	if !ok || reply.Text != "Done: created." || reply.ReplyTo != "77" {
		t.Fatalf("reply %+v in %+v", reply, out)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); got != "[confirmation] schedule.create: created" {
		t.Fatalf("the model saw %q", got)
	}
	// History stays valid: the call's own result is the placeholder, the
	// outcome is a user note after it.
	st = e.state(t)
	if len(st.Pending) != 0 {
		t.Fatalf("pending after the answer %v", st.Pending)
	}
	if d := e.decisions(t, ev.ID+":1"); !slices.Contains(d, journal.DecisionConfirm) || !slices.Contains(d, journal.DecisionApproved) {
		t.Fatalf("journal decisions %v", d)
	}
	var roles []string
	for _, m := range st.Messages {
		roles = append(roles, string(m.Role))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant,user,assistant" || !strings.Contains(st.Messages[2].Content, "awaiting confirmation") {
		t.Fatalf("history %v", roles)
	}
}

func TestDenyResumesWithDecline(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, ev := e.ask(t, "schedule.create")
	e.smart.push(say("OK, I will not."))
	out := e.turn(t, press(owner, q.Buttons[1].ID, "77"))
	if len(side.calls) != 0 {
		t.Fatal("a denied call ran")
	}
	if edit, ok := findOut(out, isEdit); !ok || !strings.HasSuffix(edit.Text, "Declined.") {
		t.Fatalf("edit in %+v", out)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); got != "[confirmation] schedule.create: the owner declined" {
		t.Fatalf("the model saw %q", got)
	}
	if d := e.decisions(t, ev.ID+":1"); !slices.Contains(d, journal.DecisionDenied) {
		t.Fatalf("journal decisions %v", d)
	}
}

// A decision event handled twice (the first run crashed before its
// replies and state were stored) runs the call once: the press is the
// same, and the stored outcome is reused.
func TestRerunDecisionEventRunsOnce(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	ev := press(owner, q.Buttons[0].ID, "77")
	e.smart.push(say("Done."), say("Done."))
	first, _, err := e.a.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	second := e.turn(t, ev)
	if len(side.calls) != 1 {
		t.Fatalf("the call ran %d times", len(side.calls))
	}
	r1, _ := findOut(first, isReply)
	r2, ok := findOut(second, isReply)
	if !ok || r1.IdemKey != r2.IdemKey || r2.Text != "Done." {
		t.Fatalf("replies %+v and %+v", r1, r2)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); got != "[confirmation] schedule.create: created" {
		t.Fatalf("the rerun told the model %q", got)
	}
}

// Someone else's press, or an unknown button, changes nothing and starts
// no turn.
func TestPressNotYoursDoesNothing(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	calls := len(e.smart.requests())
	for _, ev := range []event.Event{press("telegram:user:7", q.Buttons[0].ID, "77"), press(owner, "NOSUCHBUTTON00000000000000", "77")} {
		if out := e.turn(t, ev); len(out) != 0 {
			t.Fatalf("out %+v", out)
		}
	}
	if len(side.calls) != 0 || len(e.smart.requests()) != calls {
		t.Fatal("a refused press ran something")
	}
	if len(e.state(t).Pending) != 1 {
		t.Fatal("a refused press changed the pending list")
	}
}

func TestSecondPressSaysAlreadyHandled(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	calls := len(e.smart.requests())
	out := e.turn(t, press(owner, q.Buttons[1].ID, "77"))
	if len(out) != 1 || out[0].Edit != "77" || !strings.HasSuffix(out[0].Text, "Already handled.") {
		t.Fatalf("out %+v", out)
	}
	if len(side.calls) != 1 || len(e.smart.requests()) != calls {
		t.Fatal("a second press ran something")
	}
}

func TestLatePressSaysExpired(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	out := e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	if len(out) != 1 || !strings.HasSuffix(out[0].Text, "Expired.") || len(side.calls) != 0 {
		t.Fatalf("out %+v, calls %d", out, len(side.calls))
	}
}

// expired is the event the Expirer puts for an expired call.
func expired(t *testing.T, e *confirmEnv) event.Event {
	t.Helper()
	items, err := e.pending.ExpireDue(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("ExpireDue = %+v, %v", items, err)
	}
	p, _ := json.Marshal(event.ToolResult{Pending: items[0].ID})
	return event.Event{ID: "01EXP" + items[0].ID[:4], IdemKey: "pending:" + items[0].ID + ":expired", Source: items[0].Transport,
		Conv: items[0].Conv, Kind: event.KindToolResult, Payload: p, At: e.now}
}

func TestExpiryResumesWithExpired(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	_, ev := e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(say("The owner did not answer."))
	out := e.turn(t, expired(t, e))
	reply, ok := findOut(out, isReply)
	if !ok || reply.Text != "The owner did not answer." || reply.Transport != "telegram" || len(side.calls) != 0 {
		t.Fatalf("out %+v", out)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); !strings.HasPrefix(got, "[confirmation] schedule.create: ") || !strings.Contains(got, "did not answer") {
		t.Fatalf("the model saw %q", got)
	}
	if d := e.decisions(t, ev.ID+":1"); !slices.Contains(d, journal.DecisionExpired) {
		t.Fatalf("journal decisions %v", d)
	}
	if st := e.state(t); len(st.Pending) != 0 {
		t.Fatalf("pending %v", st.Pending)
	}
}

func TestQuickActionEditsWithoutAModel(t *testing.T) {
	e := newConfirmEnv(t)
	var got []pending.QuickPress
	e.deps.QuickActions = map[string]pending.QuickAction{"test.delete": func(_ context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
		got = append(got, p)
		return pending.QuickEdit{Text: "Deleted.", Buttons: []event.Button{{ID: "B2", Label: "Undo"}}}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	b := pending.Button{ID: pending.NewButtonID(), Action: "test.delete", Label: "Delete", Principal: owner, Conv: "telegram:42",
		Args: json.RawMessage(`{"job":"j1"}`), Expires: e.now.Add(time.Hour)}
	if err := e.pending.AddButtons(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ev := press(owner, b.ID, "90")
	out := e.turn(t, ev)
	if len(got) != 1 || string(got[0].Button.Args) != `{"job":"j1"}` || got[0].Transport != "telegram" || got[0].Event != ev.ID {
		t.Fatalf("action got %+v", got)
	}
	if len(out) != 1 || out[0].Edit != "90" || out[0].Text != "Deleted." || len(out[0].Buttons) != 1 || len(e.smart.requests()) != 0 {
		t.Fatalf("out %+v", out)
	}
}

// The approved result of a tool that brings outside content reaches the
// model as data and taints the session; addresses in it do not become
// known, since the owner did not write them.
func TestApprovedExternalResultIsWrappedAndTaints(t *testing.T) {
	page := &sideTool{name: "web.fetch", result: tool.Result{Text: "see https://evil.example/x", External: true}, class: tool.ClassExternal}
	e := newConfirmEnv(t, page)
	q, _ := e.ask(t, "web.fetch")
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	st := e.state(t)
	if !st.Tainted {
		t.Fatal("not tainted")
	}
	reqs := e.smart.requests()
	want := "[confirmation] web.fetch: " + tool.WrapExternal("web.fetch", "see https://evil.example/x")
	if got := lastUser(reqs[len(reqs)-1]); got != want {
		t.Fatalf("the model saw %q", got)
	}
	if knownURLs(st.Messages).has("https://evil.example/x") {
		t.Fatal("an address from a fetched page became known")
	}
}

func TestStalePendingIDsAreDropped(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	raw, _ := json.Marshal(State{V: 2, Session: "s", Started: t0, Last: t0, Pending: []string{"gone"}})
	e.states.m["telegram:42"] = raw
	e.turn(t, message(owner, "Igor", "hi"))
	if st := e.state(t); len(st.Pending) != 0 {
		t.Fatalf("pending %v", st.Pending)
	}
}

func TestResumeDropsStalePendingIDs(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	ctx := context.Background()
	old := State{V: 2, Session: "old", Started: t0, Last: t0, Pending: []string{"gone"},
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Igor: earlier"}}}
	if err := e.archive.Put(ctx, "telegram:42", old, "new"); err != nil {
		t.Fatal(err)
	}
	e.turn(t, message(owner, "Igor", "/resume 1"))
	if st := e.state(t); st.Session != "old" || len(st.Pending) != 0 {
		t.Fatalf("state %+v", st)
	}
}

// outTool is an approved tool whose result is outside content and that
// sends a message, from a class that does not say so.
type outTool struct{ sideTool }

func (o *outTool) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	o.calls = append(o.calls, c)
	return tool.Result{Text: "remote text", External: true,
		Out: []event.Outgoing{{IdemKey: c.Key() + ":send", Transport: "telegram", Conv: "telegram:7", Text: "hi"}}}, nil
}

// A rerun after the outcome was stored gives back the call's messages and
// its outside-content flag, though the call does not run again.
func TestRerunKeepsTheOutcome(t *testing.T) {
	o := &outTool{name: "message.send", class: tool.ClassSideEffect}
	e := newConfirmEnv(t, o)
	q, ev := e.ask(t, "message.send")
	pr := press(owner, q.Buttons[0].ID, "77")
	e.smart.push(say("Sent."), say("Sent."))
	if _, _, err := e.a.Handle(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	out := e.turn(t, pr)
	if len(o.calls) != 1 {
		t.Fatalf("the call ran %d times", len(o.calls))
	}
	if sent, ok := findOut(out, func(m event.Outgoing) bool { return m.IdemKey == ev.ID+":1:send" }); !ok || sent.Text != "hi" {
		t.Fatalf("the call's message is lost on the rerun: %+v", out)
	}
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); got != "[confirmation] message.send: "+tool.WrapExternal("message.send", "remote text") {
		t.Fatalf("the rerun told the model %q", got)
	}
	if !e.state(t).Tainted {
		t.Fatal("the rerun did not taint")
	}
}

// A refused press on a quick-action button removes the buttons only: the
// message is a list entry the agent cannot write again.
func TestRefusedQuickPressKeepsTheText(t *testing.T) {
	e := newConfirmEnv(t)
	e.deps.QuickActions = map[string]pending.QuickAction{"test.delete": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		return pending.QuickEdit{Text: "Deleted."}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	ctx := context.Background()
	mk := func(action string, expires time.Time) pending.Button {
		b := pending.Button{ID: pending.NewButtonID(), Action: action, Label: "Delete", Principal: owner, Conv: "telegram:42", Expires: expires}
		if err := e.pending.AddButtons(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	used := mk("test.delete", e.now.Add(time.Hour))
	e.turn(t, press(owner, used.ID, "90"))
	late := mk("test.delete", e.now.Add(time.Minute))
	unknown := mk("test.gone", e.now.Add(time.Hour))
	e.now = e.now.Add(2 * time.Minute)
	for name, b := range map[string]pending.Button{"used": used, "expired": late, "no action": unknown} {
		out := e.turn(t, press(owner, b.ID, "90"))
		if len(out) != 1 || out[0].Edit != "90" || out[0].Text != "" || len(out[0].Buttons) != 0 {
			t.Errorf("%s: out %+v, want an edit of the buttons only", name, out)
		}
	}
}

// An expiry event for a call that was answered meanwhile tells what
// happened, not "expired".
func TestExpiryOfAnAnsweredCallUsesItsOutcome(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.smart.push(say("Done."))
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	it, err := e.pending.Get(context.Background(), e.pendingID(t, q))
	if err != nil || it.Status != pending.StatusDone {
		t.Fatalf("item %+v, %v", it, err)
	}
	// Put the id back as if the press turn had not been stored, then
	// deliver an expiry event for it.
	raw, _ := json.Marshal(State{V: 2, Session: "s", Started: t0, Last: t0, Pending: []string{it.ID}})
	e.states.m["telegram:42"] = raw
	p, _ := json.Marshal(event.ToolResult{Pending: it.ID})
	e.smart.push(say("ok"))
	e.turn(t, event.Event{ID: "01EXPDONE", IdemKey: "pending:" + it.ID + ":expired", Source: "telegram", Conv: "telegram:42",
		Kind: event.KindToolResult, Payload: p, At: e.now})
	reqs := e.smart.requests()
	if got := lastUser(reqs[len(reqs)-1]); got != "[confirmation] schedule.create: created" {
		t.Fatalf("the model saw %q", got)
	}
}

// pendingID finds the pending call of question q.
func (e *confirmEnv) pendingID(t *testing.T, q event.Outgoing) string {
	t.Helper()
	var id string
	if err := e.db.QueryRow(`SELECT pending FROM pending_buttons WHERE id = ?`, q.Buttons[0].ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A question a quick action asked (Run now of a task that may act) runs
// its call when approved and edits the question, but no model turn
// follows: the conversation never waited for it.
func TestApprovedQuickQuestionDoesNotResumeTheModel(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	ctx := context.Background()
	c := tool.Call{TurnID: "BUTTON1", N: 1, Conv: "telegram:42", Transport: "telegram", Principal: owner, Name: "schedule.create",
		Args: json.RawMessage(`{}`)}
	id, err := e.pending.Ask(ctx, c, "Run the task now?")
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.pending.Outgoing(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out := e.turn(t, press(owner, q.Buttons[0].ID, "91"))
	if len(side.calls) != 1 || side.calls[0].Key() != "BUTTON1:1" || !side.calls[0].Admin {
		t.Fatalf("calls %+v", side.calls)
	}
	if len(out) != 1 || out[0].Edit != "91" || out[0].Text != "Run the task now?\n\nApproved." || len(e.smart.requests()) != 0 {
		t.Fatalf("out %+v", out)
	}
}
