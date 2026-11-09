package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	return newConfirmEnvOn(t, newEnv(t, Config{}, nil), hs...)
}

// newConfirmEnvOn is newConfirmEnv over an env made earlier, for tools
// that need its database or pending store.
func newConfirmEnvOn(t *testing.T, e *env, hs ...tool.Handler) *confirmEnv {
	t.Helper()
	reg := registry(t, hs...)
	e.deps.Tools = reg
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
	p, _ := json.Marshal(event.Decision{Button: button, From: from, Name: "Igor", Ref: ref, Query: fmt.Sprintf("cq%d", seq)})
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

// noAcks is out without the toasts that answer presses (ruling R40).
func noAcks(out []event.Outgoing) []event.Outgoing {
	return slices.DeleteFunc(slices.Clone(out), func(o event.Outgoing) bool { return o.Ack != "" })
}

func isEdit(o event.Outgoing) bool  { return o.Edit != "" }
func isReply(o event.Outgoing) bool { return o.Edit == "" && o.Ack == "" && len(o.Buttons) == 0 }

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
	if len(side.calls) != 1 || side.calls[0].Key() != ev.ID+":1" || !side.calls[0].Admin || side.calls[0].Transport != "telegram" ||
		!side.calls[0].Approved {
		t.Fatalf("calls %+v", side.calls)
	}
	edit, ok := findOut(out, isEdit)
	if !ok || edit.Edit != "77" || edit.Folded != q.Text || edit.Text != closing("Approved", e.now) || len(edit.Buttons) != 0 {
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
	if edit, ok := findOut(out, isEdit); !ok || edit.Text != closing("Declined", e.now) {
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
		if out := noAcks(e.turn(t, ev)); len(out) != 0 {
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

// A second press of an answered question (the other button, or the same
// one tapped twice) runs nothing and edits the question to how it really
// ended, not to a line that hides it (M3b final review).
func TestSecondPressShowsTheRealOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		first, second int
		want          string
	}{
		"approve, then deny": {0, 1, "Approved"},
		"approve twice":      {0, 0, "Approved"},
		"deny, then approve": {1, 0, "Declined"},
		"deny twice":         {1, 1, "Declined"},
	} {
		side := newSideTool()
		e := newConfirmEnv(t, side)
		q, _ := e.ask(t, "schedule.create")
		e.turn(t, press(owner, q.Buttons[tc.first].ID, "77"))
		calls, ran := len(e.smart.requests()), len(side.calls)
		out := noAcks(e.turn(t, press(owner, q.Buttons[tc.second].ID, "77")))
		if len(out) != 1 || out[0].Edit != "77" || out[0].Text != closing(tc.want, e.now) || out[0].Folded != q.Text || len(out[0].Buttons) != 0 {
			t.Errorf("%s: out %+v", name, out)
		}
		if len(side.calls) != ran || len(e.smart.requests()) != calls {
			t.Errorf("%s: a second press ran something", name)
		}
	}
}

// A second tap of a quick-action button (Details tapped twice) leaves the
// message as the first tap made it: its new keyboard stays.
func TestSecondTapOfAQuickButtonKeepsTheFirstEdit(t *testing.T) {
	e := newConfirmEnv(t)
	e.deps.QuickActions = map[string]pending.QuickAction{"test.details": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		return pending.QuickEdit{Text: "Details.", Buttons: []event.Button{{ID: "B2", Label: "Delete"}}}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	b := pending.Button{ID: pending.NewButtonID(), Action: "test.details", Label: "Details", Principal: owner, Conv: "telegram:42",
		Expires: e.now.Add(time.Hour)}
	if err := e.pending.AddButtons(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if out := noAcks(e.turn(t, press(owner, b.ID, "90"))); len(out) != 1 || len(out[0].Buttons) != 1 {
		t.Fatalf("first tap %+v", out)
	}
	if out := noAcks(e.turn(t, press(owner, b.ID, "90"))); len(out) != 0 {
		t.Fatalf("the second tap edited the message: %+v", out)
	}
}

// Quick actions re-check the admin role at press time: a button made for
// an admin who has since lost the role does nothing (M3b final review).
func TestQuickActionNeedsAnAdminAtPressTime(t *testing.T) {
	e := newConfirmEnv(t)
	ran := 0
	e.deps.QuickActions = map[string]pending.QuickAction{"test.delete": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		ran++
		return pending.QuickEdit{Text: "Deleted."}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	b := pending.Button{ID: pending.NewButtonID(), Action: "test.delete", Label: "Delete", Principal: owner, Conv: "telegram:42",
		Expires: e.now.Add(time.Hour)}
	if err := e.pending.AddButtons(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	e.admin = false
	if out := noAcks(e.turn(t, press(owner, b.ID, "90"))); len(out) != 0 || ran != 0 {
		t.Fatalf("a former admin's press: out %+v, ran %d", out, ran)
	}
}

func TestLatePressSaysExpired(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	out := noAcks(e.turn(t, press(owner, q.Buttons[0].ID, "77")))
	if len(out) != 1 || !strings.HasPrefix(out[0].Text, "Expired, ") || out[0].Folded != q.Text || len(side.calls) != 0 {
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
	for name, b := range map[string]pending.Button{"expired": late, "no action": unknown} {
		out := noAcks(e.turn(t, press(owner, b.ID, "90")))
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
	if len(out) != 1 || out[0].Edit != "91" || out[0].Text != closing("Approved", e.now) || out[0].Folded != "Run the task now?" || len(e.smart.requests()) != 0 {
		t.Fatalf("out %+v", out)
	}
}

// A call parked before arguments were checked (ruling R18) is checked
// when it is approved or declined: a bad one does not run and is
// journalled forbidden.
func TestStoredCallWithBadArgsIsRefusedOnResume(t *testing.T) {
	for _, button := range []int{0, 1} { // Approve, Deny
		side := newSideTool()
		e := newConfirmEnv(t, side)
		q, ev := e.ask(t, "schedule.create")
		if _, err := e.db.ExecContext(context.Background(), `UPDATE pending_calls SET args = ?`, `{"Text":"smuggled"}`); err != nil {
			t.Fatal(err)
		}
		e.smart.push(say("ok"))
		e.turn(t, press(owner, q.Buttons[button].ID, "77"))
		if len(side.calls) != 0 {
			t.Fatalf("button %d: the stored call ran: %+v", button, side.calls)
		}
		if d := e.decisions(t, ev.ID+":1"); len(d) == 0 || d[len(d)-1] != journal.DecisionForbidden {
			t.Fatalf("button %d: journal %v", button, d)
		}
		reqs := e.smart.requests()
		if got := lastUser(reqs[len(reqs)-1]); !strings.Contains(got, tool.ErrBadArgs.Error()) {
			t.Fatalf("button %d: the model saw %q", button, got)
		}
	}
}

// Ruling R33: an expiry continuation runs without tools; the model only
// tells the owner the request expired, so a tainted turn cannot park
// again every TTL without the owner.
func TestExpiryTurnOffersNoTools(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(say("The request expired."))
	e.turn(t, expired(t, e))
	reqs := e.smart.requests()
	if last := reqs[len(reqs)-1]; len(last.Tools) != 0 || last.Exec != nil {
		t.Fatalf("the expiry turn offered %d tools, exec set %v", len(last.Tools), last.Exec != nil)
	}
}

// Ruling R33 on every path: a model that answers an expiry turn with a
// tool call anyway gets none run, and the call does not stay in the
// history unanswered.
func TestExpiryTurnRunsNoToolTheModelCalls(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(func(ctx context.Context, r provider.ChatRequest) (provider.ChatResponse, error) {
		resp, err := callWith("schedule.create", `{}`)(ctx, r)
		resp.Message.Content = "The request expired."
		return resp, err
	})
	e.turn(t, expired(t, e))
	if len(side.calls) != 0 {
		t.Fatalf("a tool ran in the expiry turn: %d calls", len(side.calls))
	}
	for _, m := range e.state(t).Messages {
		if len(m.ToolCalls) != 0 && m.Content == "The request expired." {
			t.Fatalf("the unoffered call stayed in the history: %+v", m)
		}
	}
}

// A model that answers an expiry turn only with a tool call (none was
// offered) leaves no text: the owner still gets the fixed expiry reply,
// not the step-limit apology.
func TestExpiryTurnWithOnlyAToolCallSaysItExpired(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(callWith("schedule.create", `{}`))
	out := e.turn(t, expired(t, e))
	if len(side.calls) != 0 {
		t.Fatalf("a tool ran in the expiry turn: %d calls", len(side.calls))
	}
	if r, ok := findOut(out, isReply); !ok || r.Text != replyExpired {
		t.Fatalf("out %+v, want the reply %q", out, replyExpired)
	}
}

// Ruling R33: a resumed turn passes the daily limit like a message: the
// approved call runs (the owner decided it), but no model turn follows
// over the limit, and the outcome still joins the history.
func TestResumedTurnOverTheDailyLimitIsRefused(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	e.a = New(overDefaults(Config{DailyTurns: 1}), e.deps)
	q, _ := e.ask(t, "schedule.create")
	calls := len(e.smart.requests())
	ev := press(owner, q.Buttons[0].ID, "77")
	out := e.turn(t, ev)
	if len(side.calls) != 1 {
		t.Fatalf("the approved call ran %d times", len(side.calls))
	}
	if len(e.smart.requests()) != calls {
		t.Fatal("the model was called over the daily limit")
	}
	if r, ok := findOut(out, isReply); !ok || r.Text != replyDailyLimit {
		t.Fatalf("out %+v", out)
	}
	if tr, _ := e.turns.Get(context.Background(), ev.ID); tr.Outcome != "daily_limit" {
		t.Fatalf("outcome %q", tr.Outcome)
	}
	st := e.state(t)
	if last := st.Messages[len(st.Messages)-1]; last.Content != "[confirmation] schedule.create: created" || len(st.Pending) != 0 {
		t.Fatalf("history ends %+v, pending %v", last, st.Pending)
	}
}

// Ruling R33: a call parked in a resumed turn carries its parent's depth
// plus one, so a chain of approvals that keeps asking again ends at
// policy.DefaultMaxDepth: the call past it is forbidden, not asked.
func TestResumeChainDeeperThanMaxDepthIsForbidden(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, ev := e.ask(t, "schedule.create")
	for hop := 1; ; hop++ {
		e.smart.push(callTool("schedule.create"), say("asked again"))
		out := e.turn(t, press(owner, q.Buttons[0].ID, "77"))
		next, asked := findOut(out, func(o event.Outgoing) bool { return len(o.Buttons) > 0 })
		if hop <= policy.DefaultMaxDepth {
			if !asked {
				t.Fatalf("hop %d: no question; out %+v", hop, out)
			}
			q = next
			continue
		}
		if asked {
			t.Fatalf("hop %d: a call deeper than MaxDepth was asked: %+v", hop, next)
		}
		break
	}
	entries, err := e.journal.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Decision != journal.DecisionForbidden || last.Tool != "schedule.create" || strings.HasPrefix(last.Key, ev.ID) {
		t.Fatalf("last journal entry %+v, want the deep call forbidden", last)
	}
}

// toolResults are the tool messages of the last request the model got.
func toolResults(req provider.ChatRequest) []string {
	var out []string
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool {
			out = append(out, m.Content)
		}
	}
	return out
}

// The live run of 2026-11-03: a later turn asked the same call again
// while the first question still waited. The same call now makes no
// second question; once the first is answered or expired it asks again
// (ruling R38).
func TestSameCallWaitingIsNotAskedTwice(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")

	e.smart.push(callTool("schedule.create"), say("It is waiting for you."))
	out := e.turn(t, message(owner, "Igor", "and again"))
	if _, asked := findOut(out, func(o event.Outgoing) bool { return len(o.Buttons) > 0 }); asked {
		t.Fatalf("a second question: %+v", out)
	}
	reqs := e.smart.requests()
	if got := toolResults(reqs[len(reqs)-1]); got[len(got)-1] != policy.AlreadyWaiting {
		t.Fatalf("the model read %q", got[len(got)-1])
	}
	if st := e.state(t); len(st.Pending) != 1 {
		t.Fatalf("pending %v: want the one waiting call", st.Pending)
	}

	// Answered: it runs once, and the same call asks again.
	e.smart.push(say("Done."))
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	if len(side.calls) != 1 {
		t.Fatalf("the call ran %d times", len(side.calls))
	}
	q2, _ := e.ask(t, "schedule.create")
	if q2.IdemKey == q.IdemKey {
		t.Fatal("no new question after the answer")
	}
	// Expired: the same call asks again.
	e.now = e.now.Add(pending.DefaultTTL)
	q3, _ := e.ask(t, "schedule.create")
	if q3.IdemKey == q2.IdemKey {
		t.Fatal("no new question after the expiry")
	}
}

// The live run of 2026-11-03: after a task was deleted by its button, the
// model thought it had vanished. A quick action that changes something
// leaves a note in the conversation's history, which the next turn
// reads; its addresses do not become known (ruling R42).
func TestQuickActionNoteJoinsTheHistory(t *testing.T) {
	e := newConfirmEnv(t)
	e.deps.QuickActions = map[string]pending.QuickAction{"test.delete": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		return pending.QuickEdit{Text: "Deleted.", Note: "the owner deleted task j1: get https://bank.example/x"}, nil
	}, "test.details": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		return pending.QuickEdit{Text: "Details"}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	var ids []string
	for _, action := range []string{"test.details", "test.delete"} {
		b := pending.Button{ID: pending.NewButtonID(), Action: action, Label: action, Principal: owner, Conv: "telegram:42",
			Args: json.RawMessage(`{}`), Expires: e.now.Add(time.Hour)}
		if err := e.pending.AddButtons(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	e.turn(t, press(owner, ids[0], "90"))
	if st := e.state(t); len(st.Messages) != 0 {
		t.Fatalf("details left a note: %+v", st.Messages)
	}
	e.turn(t, press(owner, ids[1], "91"))
	e.turn(t, press(owner, ids[1], "91")) // a second tap adds nothing
	e.smart.push(say("You have no tasks now."))
	e.turn(t, message(owner, "Igor", "show my reminders"))
	var notes []string
	for _, m := range e.smart.requests()[0].Messages {
		if m.Role == provider.RoleUser && strings.HasPrefix(m.Content, "[button] ") {
			notes = append(notes, m.Content)
		}
	}
	if len(notes) != 1 || notes[0] != "[button] the owner deleted task j1: get https://bank.example/x" {
		t.Fatalf("notes %q", notes)
	}
	if knownURLs(e.state(t).Messages).has("https://bank.example/x") {
		t.Fatal("an address in a button note became known")
	}
}

// queryOf is the press's callback query id.
func queryOf(t *testing.T, ev event.Event) string {
	t.Helper()
	var d event.Decision
	if err := json.Unmarshal(ev.Payload, &d); err != nil {
		t.Fatal(err)
	}
	return d.Query
}

// watchTool is a side-effect tool that, when it runs, records what the
// outbox held then, and sends a message.
type watchTool struct {
	sideTool
	early *enqueued
	seen  []event.Outgoing
}

func (w *watchTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	w.seen = w.early.got()
	_, _ = w.sideTool.Run(ctx, c)
	return tool.Result{Text: "sent", Out: []event.Outgoing{{IdemKey: c.Key() + ":send", Transport: "telegram", Conv: "telegram:42", Text: "the effect"}}}, nil
}

// A valid press is acknowledged before the call runs: a toast and the
// question with a status line in place of its buttons are in the outbox
// by then. The turn's own messages are the call's effects, then the
// final edit with the outcome, then the model's answer (ruling R40).
func TestPressIsAcknowledgedBeforeTheCallRuns(t *testing.T) {
	e := newConfirmEnv(t)
	w := &watchTool{sideTool: *newSideTool(), early: e.early}
	e = newConfirmEnvOn(t, e.env, w)
	q, _ := e.ask(t, "schedule.create")
	e.smart.push(say("Done."))
	ev := press(owner, q.Buttons[0].ID, "77")
	out := e.turn(t, ev)
	ack := event.Outgoing{IdemKey: ev.IdemKey + ":ack", Transport: "telegram", Conv: "telegram:42", Text: "Approved", Ack: queryOf(t, ev)}
	status := event.Outgoing{IdemKey: ev.IdemKey + ":status", Transport: "telegram", Conv: "telegram:42", Edit: "77",
		Text: "⏳ Approved, running…", Folded: q.Text}
	if len(w.calls) != 1 || len(w.seen) != 2 || !reflect.DeepEqual(w.seen[0], ack) || !reflect.DeepEqual(w.seen[1], status) {
		t.Fatalf("the outbox held %+v when the call ran", w.seen)
	}
	var kinds []string
	for _, o := range out {
		kinds = append(kinds, o.Text)
	}
	if len(out) != 3 || out[0].Text != "the effect" || out[1].Edit != "77" || out[1].Text != closing("Approved", e.now) || out[1].Folded != q.Text ||
		out[1].IdemKey != ev.IdemKey+":edit" || out[2].Text != "Done." {
		t.Fatalf("turn out %q", kinds)
	}
	// A rerun of the same press stores nothing new and runs nothing again.
	e.smart.push(say("Done."))
	e.turn(t, ev)
	if len(w.calls) != 1 {
		t.Fatalf("the call ran %d times", len(w.calls))
	}
}

// A denied call is acknowledged at once too; a failed call's final line
// says so.
func TestDenyAndFailureAreShown(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.smart.push(say("OK."))
	ev := press(owner, q.Buttons[1].ID, "77")
	e.turn(t, ev)
	early := e.early.got()
	if len(early) != 2 || early[0].Text != "Declined" || early[0].Ack == "" || early[1].Text != closing("Declined", e.now) || early[1].Folded != q.Text {
		t.Fatalf("early %+v", early)
	}
}

// Presses that do not take keep today's handling, with a toast saying
// why: someone else's (no edit), a late one and a second one; none runs
// the call or stores anything early.
func TestRefusedPressesGetAToast(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	toast := func(out []event.Outgoing) (string, bool) {
		var text string
		edited := false
		for _, o := range out {
			if o.Ack != "" {
				text = o.Text
			}
			edited = edited || o.Edit != ""
		}
		return text, edited
	}
	if got, edited := toast(e.turn(t, press("telegram:user:43", q.Buttons[0].ID, "77"))); got != "This button is not yours." || edited {
		t.Fatalf("someone else's press: toast %q, edited %v", got, edited)
	}
	if got, _ := toast(e.turn(t, press(owner, "UNKNOWNBUTTON0000000000000", "77"))); got != "This button no longer works." {
		t.Fatalf("an unknown button: toast %q", got)
	}
	e.smart.push(say("Done."))
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	n := len(e.early.got())
	out := e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	if got, edited := toast(out); got != "Already handled." || !edited || len(side.calls) != 1 {
		t.Fatalf("a double press: toast %q, edited %v, ran %d", got, edited, len(side.calls))
	}
	q2, _ := e.ask(t, "schedule.create")
	e.now = e.now.Add(pending.DefaultTTL + time.Second)
	out = e.turn(t, press(owner, q2.Buttons[0].ID, "78"))
	if got, edited := toast(out); got != "Expired." || !edited || len(side.calls) != 1 {
		t.Fatalf("a late press: toast %q, edited %v, ran %d", got, edited, len(side.calls))
	}
	if len(e.early.got()) != n {
		t.Fatalf("a refused press stored early messages: %+v", e.early.got()[n:])
	}
}

// A quick action's press gets its toast before the action runs.
func TestQuickPressIsAcknowledgedFirst(t *testing.T) {
	e := newConfirmEnv(t)
	var seen []event.Outgoing
	e.deps.QuickActions = map[string]pending.QuickAction{"test.delete": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		seen = e.early.got()
		return pending.QuickEdit{Text: "Deleted."}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	b := pending.Button{ID: pending.NewButtonID(), Action: "test.delete", Label: "Delete", Principal: owner, Conv: "telegram:42",
		Args: json.RawMessage(`{}`), Expires: e.now.Add(time.Hour)}
	if err := e.pending.AddButtons(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ev := press(owner, b.ID, "90")
	e.turn(t, ev)
	if len(seen) != 1 || seen[0].Ack != queryOf(t, ev) || seen[0].Text != "Accepted" || seen[0].IdemKey != ev.IdemKey+":ack" {
		t.Fatalf("the outbox held %+v when the action ran", seen)
	}
}

// The early acknowledgement is a convenience: an outbox that fails it
// does not stop the press.
func TestEarlyAckFailureDoesNotStopThePress(t *testing.T) {
	side := newSideTool()
	e := newConfirmEnv(t, side)
	q, _ := e.ask(t, "schedule.create")
	e.early.err = errors.New("disk full")
	e.smart.push(say("Done."))
	e.turn(t, press(owner, q.Buttons[0].ID, "77"))
	if len(side.calls) != 1 {
		t.Fatalf("the call ran %d times", len(side.calls))
	}
}

// failTool is a side-effect tool that fails, or answers with text that
// merely starts like an error.
type failTool struct {
	sideTool
	err error
}

func (f *failTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	res, _ := f.sideTool.Run(ctx, c)
	return res, f.err
}

// The final line says whether the call failed from the stored outcome's
// flag, not from its text; a second press shows the same (polish of
// R40).
func TestFinalLineFollowsTheTypedOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		err  error
		want string
	}{
		"failed":                 {"", errors.New("boom"), "Approved, but it failed"},
		"ok, text like an error": {"error: none found", nil, "Approved"},
	} {
		tl := &failTool{sideTool: *newSideTool(), err: tc.err}
		tl.result = tool.Result{Text: tc.text}
		e := newConfirmEnv(t, tl)
		q, _ := e.ask(t, "schedule.create")
		e.smart.push(say("ok"))
		out := noAcks(e.turn(t, press(owner, q.Buttons[0].ID, "77")))
		edit, _ := findOut(out, isEdit)
		if edit.Text != closing(tc.want, e.now) || edit.Folded != q.Text {
			t.Errorf("%s: final edit %q", name, edit.Text)
		}
		again, _ := findOut(noAcks(e.turn(t, press(owner, q.Buttons[0].ID, "77"))), isEdit)
		if again.Text != closing(tc.want, e.now) || again.Folded != q.Text {
			t.Errorf("%s: second press edit %q", name, again.Text)
		}
	}
}

// closing is the line of a folded question decided at at, in the test agent's
// zone (R-POL-9).
func closing(line string, at time.Time) string { return line + ", " + at.UTC().Format("15:04") }

// The time on the line is the press's, in the node's zone, and a later press of
// the same question does not move it (R-POL-9).
func TestFoldedLineKeepsTheTimeOfThePress(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	e := newConfirmEnv(t, newSideTool())
	e.a = New(overDefaults(Config{Location: kyiv}), e.deps)
	q, _ := e.ask(t, "schedule.create")
	want := "Approved, " + e.now.In(kyiv).Format("15:04")
	e.smart.push(say("ok"))
	first, _ := findOut(noAcks(e.turn(t, press(owner, q.Buttons[0].ID, "77"))), isEdit)
	if first.Text != want {
		t.Fatalf("first press: %q, want %q", first.Text, want)
	}
	e.now = e.now.Add(3 * time.Minute)
	again, _ := findOut(noAcks(e.turn(t, press(owner, q.Buttons[0].ID, "77"))), isEdit)
	if again.Text != want {
		t.Fatalf("second press three minutes later: %q, want %q", again.Text, want)
	}
}

// fakeRefs is where the outbox says messages went, by idempotency key.
type fakeRefs map[string]string

func (f fakeRefs) SentRef(_ context.Context, key string) (string, error) { return f[key], nil }

func (fakeRefs) SentByRef(context.Context, string, string, string) (event.Outgoing, bool, error) {
	return event.Outgoing{}, false, nil
}

// questionEdit is the edit of a message in out, if any.
func questionEdit(out []event.Outgoing) (event.Outgoing, bool) {
	for _, o := range out {
		if o.Edit != "" {
			return o, true
		}
	}
	return event.Outgoing{}, false
}

// withRefs gives the agent the question's reference as the outbox would.
func withRefs(e *confirmEnv, q event.Outgoing, ref string) {
	e.deps.Sent = fakeRefs{q.IdemKey: ref}
	e.a = New(overDefaults(Config{}), e.deps)
}

func TestExpiredQuestionClosesItself(t *testing.T) {
	e := newConfirmEnv(t, newSideTool())
	q, _ := e.ask(t, "schedule.create")
	withRefs(e, q, "91")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(say("The owner did not answer."))
	edit, ok := questionEdit(e.turn(t, expired(t, e)))
	if !ok || edit.Edit != "91" || len(edit.Buttons) != 0 || edit.Text != closing("Expired", e.now) || edit.Folded != q.Text {
		t.Fatalf("edit %+v, %v", edit, ok)
	}
}

func TestExpiredQuestionClosesInAMovedOnSession(t *testing.T) {
	e := newConfirmEnv(t, newSideTool())
	q, _ := e.ask(t, "schedule.create")
	withRefs(e, q, "91")
	e.turn(t, message(owner, "Igor", "/new"))
	before := len(e.smart.requests())
	e.now = e.now.Add(pending.DefaultTTL)
	edit, ok := questionEdit(e.turn(t, expired(t, e)))
	if !ok || edit.Edit != "91" || edit.Text != closing("Expired", e.now) || edit.Folded != q.Text {
		t.Fatalf("edit %+v, %v", edit, ok)
	}
	if got := len(e.smart.requests()); got != before {
		t.Fatalf("%d model calls for a session that moved on", got-before)
	}
}

func TestExpiredQuestionNeverDelivered(t *testing.T) {
	e := newConfirmEnv(t, newSideTool())
	q, _ := e.ask(t, "schedule.create")
	withRefs(e, q, "")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(say("The owner did not answer."))
	out := e.turn(t, expired(t, e))
	if edit, ok := questionEdit(out); ok {
		t.Fatalf("an edit of a message nobody saw: %+v", edit)
	}
	if reply, ok := findOut(out, isReply); !ok || reply.Text != "The owner did not answer." {
		t.Fatalf("out %+v", out)
	}
}

func TestAnsweredCallIsNotMarkedExpired(t *testing.T) {
	e := newConfirmEnv(t, newSideTool())
	q, _ := e.ask(t, "schedule.create")
	withRefs(e, q, "91")
	e.smart.push(say("Done."))
	e.turn(t, press(owner, q.Buttons[0].ID, "91"))
	it, err := e.pending.Get(context.Background(), e.pendingID(t, q))
	if err != nil || it.Status != pending.StatusDone {
		t.Fatalf("item %+v, %v", it, err)
	}
	p, _ := json.Marshal(event.ToolResult{Pending: it.ID})
	out := e.turn(t, event.Event{ID: "01EXPDONE", IdemKey: "pending:" + it.ID + ":expired", Source: "telegram", Conv: "telegram:42",
		Kind: event.KindToolResult, Payload: p, At: e.now})
	if edit, ok := questionEdit(out); ok {
		t.Fatalf("an approved question marked: %+v", edit)
	}
}

// A quick action can show a question in the pressed message. The edit takes
// the question's key, so the outbox can tell where the question is when its
// call expires.
func TestQuickEditKeepsTheQuestionsKey(t *testing.T) {
	e := newConfirmEnv(t)
	e.deps.QuickActions = map[string]pending.QuickAction{"ask": func(context.Context, pending.QuickPress) (pending.QuickEdit, error) {
		return pending.QuickEdit{Text: "Run?", Buttons: []event.Button{{ID: "b", Label: "Approve"}}, Key: "call:1:confirm"}, nil
	}}
	e.a = New(overDefaults(Config{}), e.deps)
	if err := e.pending.AddButtons(context.Background(), pending.Button{ID: "QB", Action: "ask", Label: "Run now", Principal: owner, Conv: "telegram:42", Expires: e.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	edit, ok := questionEdit(e.turn(t, press(owner, "QB", "55")))
	if !ok || edit.Edit != "55" || edit.IdemKey != "call:1:confirm" {
		t.Fatalf("edit %+v, %v", edit, ok)
	}
}
