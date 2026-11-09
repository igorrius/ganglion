package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
)

// ranCommands is a host runner that records what it ran and says it ended.
type ranCommands struct{ argv [][]string }

func (r *ranCommands) Exec(_ context.Context, q host.Request) (host.Answer, error) {
	r.argv = append(r.argv, q.Argv)
	return host.Answer{Done: true, Job: host.Job{Key: q.Key, Text: "exit 0"}}, nil
}

type planGate struct{}

func (planGate) Open() bool { return true }

type planNoIgnored struct{}

func (planNoIgnored) Ignored(context.Context, string, []string) (string, bool) { return "", false }

// planEnv is a confirmEnv with the real plan store, plan tools and host.exec
// over a runner that only records.
type planEnv struct {
	*confirmEnv
	plans *plan.Store
	ran   *ranCommands
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	store, err := plan.New(context.Background(), e.db, e.early, plan.WithHome("/home/o"))
	if err != nil {
		t.Fatal(err)
	}
	ran := &ranCommands{}
	exec := builtin.NewHostExec(ran, planNoIgnored{}, planGate{}, "/home/o")
	c := newConfirmEnvOn(t, e, append(builtin.NewPlanTools(store, exec), exec)...)
	c.deps.Plans = store
	c.a = New(overDefaults(Config{Home: "/home/o"}), c.deps)
	return &planEnv{confirmEnv: c, plans: store, ran: ran}
}

const cleanPlan = `{"steps":["Measure",{"text":"Clean Go's cache","argv":["go","clean","-cache"],"reason":"it is 3 GB"}]}`

// askPlan runs a turn in which the model shows a plan with a command that
// needs a press: the plan goes out as one question.
func (e *planEnv) askPlan(t *testing.T) (event.Outgoing, event.Event) {
	t.Helper()
	e.smart.push(callWith("plan.show", cleanPlan), say("I asked."))
	ev := message(owner, "Igor", "free some space")
	for _, o := range e.turn(t, ev) {
		if len(o.Buttons) > 0 {
			return o, ev
		}
	}
	t.Fatal("no question")
	return event.Outgoing{}, ev
}

// R-CMD-7, ruling 8: Approve on a plan's question does not fold it: the
// plan's own edit replaces the question and its buttons. The resumed turn's
// listed command runs as planned; one the plan did not list asks on its own.
func TestApprovedPlanKeepsItsQuestionAndRunsItsCommands(t *testing.T) {
	e := newPlanEnv(t)
	q, ev := e.askPlan(t)
	if !strings.HasPrefix(q.Text, "Run this plan?") || !strings.Contains(q.Text, "`go clean -cache` in `/home/o`") {
		t.Fatalf("question %q", q.Text)
	}
	if len(e.ran.argv) != 0 {
		t.Fatal("a command ran before the press")
	}
	e.smart.push(callWith("host.exec", `{"argv":["go","clean","-cache"],"reason":"step 2"}`),
		callWith("host.exec", `{"argv":["rm","-r","/tmp/x"],"reason":"also"}`), say("Cleaned."))
	pr := press(owner, q.Buttons[0].ID, "77")
	out := e.turn(t, pr)
	for _, o := range append(e.early.got(), out...) {
		if o.Edit == "77" {
			t.Fatalf("the plan's question was edited by the agent: %+v", o)
		}
	}
	var planEdit event.Outgoing
	for _, o := range e.early.got() {
		if o.EditOf == q.IdemKey {
			planEdit = o
		}
	}
	if planEdit.Text == "" || len(planEdit.Buttons) != 0 || !strings.Contains(planEdit.Text, "Clean Go's cache") {
		t.Fatalf("the plan's edit of its question: %+v", planEdit)
	}
	if !slices.ContainsFunc(e.early.got(), func(o event.Outgoing) bool { return o.Ack != "" && o.Text == "Approved" }) {
		t.Fatal("the press got no toast")
	}
	if len(e.ran.argv) != 1 || !slices.Equal(e.ran.argv[0], []string{"go", "clean", "-cache"}) {
		t.Fatalf("ran %v", e.ran.argv)
	}
	if d := e.decisions(t, pr.ID+":1"); !slices.Contains(d, journal.DecisionPlanned) {
		t.Fatalf("the listed command's decisions %v", d)
	}
	if d := e.decisions(t, pr.ID+":2"); !slices.Contains(d, journal.DecisionConfirm) {
		t.Fatalf("the unlisted command's decisions %v", d)
	}
	if d := e.decisions(t, ev.ID+":1"); !slices.Contains(d, journal.DecisionConfirm) || !slices.Contains(d, journal.DecisionApproved) {
		t.Fatalf("the plan's decisions %v", d)
	}
}

// A declined plan folds like any question, and approves nothing.
func TestDeclinedPlanFolds(t *testing.T) {
	e := newPlanEnv(t)
	q, _ := e.askPlan(t)
	e.smart.push(say("OK."))
	out := e.turn(t, press(owner, q.Buttons[1].ID, "77"))
	if edit, ok := findOut(out, isEdit); !ok || edit.Text != closing("Declined", e.now) || edit.Folded != q.Text {
		t.Fatalf("edit in %+v", out)
	}
	if got, err := e.plans.Approved(context.Background(), "telegram:42"); err != nil || len(got) != 0 {
		t.Fatalf("approved %+v, %v", got, err)
	}
}

// An expired plan folds like any question.
func TestExpiredPlanFolds(t *testing.T) {
	e := newPlanEnv(t)
	q, _ := e.askPlan(t)
	withRefs(e.confirmEnv, q, "91")
	e.now = e.now.Add(pending.DefaultTTL)
	e.smart.push(say("The owner did not answer."))
	edit, ok := questionEdit(e.turn(t, expired(t, e.confirmEnv)))
	if !ok || edit.Edit != "91" || edit.Text != closing("Expired", e.now) || edit.Folded != q.Text {
		t.Fatalf("edit %+v, %v", edit, ok)
	}
}

// failingPlans cannot read the approved plan.
type failingPlans struct{ NoPlans }

func (failingPlans) Approved(context.Context, string) ([]tool.Command, error) {
	return nil, errors.New("disk on fire")
}

// Fail closed: an approved plan that cannot be read approves nothing.
func TestUnreadablePlanApprovesNothing(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.Plans = failingPlans{}
	a := New(overDefaults(Config{}), e.deps)
	if a.planned(context.Background(), "telegram:42")(tool.Command{Argv: []string{"go", "clean", "-cache"}}) {
		t.Fatal("an unreadable plan approved a command")
	}
}

// Review focus 6: the approval is the conversation's and the folder's.
func TestPlannedIsTheConversationsAndTheFolders(t *testing.T) {
	e := newPlanEnv(t)
	c := tool.Call{TurnID: "e1", N: 1, Conv: "telegram:42", Transport: "telegram", Approved: true}
	cmd := tool.Command{Argv: []string{"go", "clean", "-cache"}}
	if err := e.plans.Show(context.Background(), c, []plan.Step{{Text: "x", Command: &cmd}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if !e.a.planned(ctx, "telegram:42")(cmd) {
		t.Fatal("the approved command is not planned")
	}
	if e.a.planned(ctx, "telegram:7")(cmd) {
		t.Fatal("another conversation's plan approved it")
	}
	if e.a.planned(ctx, "telegram:42")(tool.Command{Argv: cmd.Argv, Dir: "/elsewhere"}) {
		t.Fatal("the command in another folder is planned")
	}
}

// A command without dir runs in home: a plan step without dir approves
// host.exec with dir set to home, and a step with dir home approves one
// without dir; another folder still asks.
func TestAPlannedCommandMatchesHomeEitherWay(t *testing.T) {
	for _, c := range []struct {
		stepDir, callDir string
		planned          bool
	}{{"", "/home/o", true}, {"/home/o", "", true}, {"", "/srv", false}} {
		e := newPlanEnv(t)
		cmd := tool.Command{Argv: []string{"go", "clean", "-cache"}, Dir: c.stepDir}
		show := tool.Call{TurnID: "e1", N: 1, Conv: "telegram:42", Transport: "telegram", Approved: true}
		if err := e.plans.Show(context.Background(), show, []plan.Step{{Text: "x", Command: &cmd}}); err != nil {
			t.Fatal(err)
		}
		if got := e.a.planned(context.Background(), "telegram:42")(tool.Command{Argv: cmd.Argv, Dir: c.callDir}); got != c.planned {
			t.Errorf("step dir %q, call dir %q: planned %v, want %v", c.stepDir, c.callDir, got, c.planned)
		}
	}
}

// End to end: the pressed plan's step has no dir, and the model repeats the
// command with dir set to home: it runs as planned, without a second question.
func TestAPlanWithoutAFolderRunsTheCommandInHome(t *testing.T) {
	e := newPlanEnv(t)
	q, _ := e.askPlan(t)
	e.smart.push(callWith("host.exec", `{"argv":["go","clean","-cache"],"dir":"/home/o","reason":"step 2"}`), say("Cleaned."))
	pr := press(owner, q.Buttons[0].ID, "77")
	e.turn(t, pr)
	if len(e.ran.argv) != 1 {
		t.Fatalf("ran %v", e.ran.argv)
	}
	if d := e.decisions(t, pr.ID+":1"); !slices.Contains(d, journal.DecisionPlanned) {
		t.Fatalf("the command in home: decisions %v, want planned", d)
	}
}
