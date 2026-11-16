package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// planTool stores a plan message through the outbox under the call's context, as
// plan.show does.
type planTool struct {
	fakeTool
	add *addEnv
}

func (p *planTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	err := p.add.q.Enqueue(ctx, event.Outgoing{IdemKey: "plan:" + c.Conv + ":show", Transport: "telegram", Conv: c.Conv, Text: "1. a"})
	return tool.Result{Text: "shown"}, err
}

func originOf(t *testing.T, out []event.Outgoing, text string) event.Origin {
	t.Helper()
	for _, m := range out {
		if m.Text == text {
			if m.Origin == nil {
				t.Fatalf("%q has no origin", text)
			}
			return *m.Origin
		}
	}
	t.Fatalf("no message %q in %+v", text, out)
	return event.Origin{}
}

// R-DLV-14: a turn's reply carries its session and whether it was tainted.
func TestTurnReplyCarriesItsOrigin(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("hello"))
	ev := message(owner, "Igor", "hi")
	out := e.turn(t, ev)
	want := event.Origin{Kind: event.OriginTurn, Session: ev.ID}
	if got := originOf(t, out, "hello"); got != want {
		t.Errorf("origin %+v, want %+v", got, want)
	}
}

// R-DLV-14: what a turn stores before outside content enters is clean, and its
// reply after is tainted.
func TestOriginFollowsTheTaintOfTheMoment(t *testing.T) {
	page := &fakeTool{name: "web.fetch", result: tool.Result{Text: "a page", External: true}}
	show := &planTool{}
	show.name = "plan.show"
	e := newAddEnv(t, Config{}, page, show)
	show.add = e
	e.smart.push(callTools("plan.show"), callTools("web.fetch"), say("done"))
	e.put(t, message(owner, "Igor", "make a plan and read"))
	e.next(t)
	var plan, reply *event.Origin
	for _, m := range e.outbox(t) {
		switch m.Text {
		case "1. a":
			plan = m.Origin
		case "done":
			reply = m.Origin
		}
	}
	if plan == nil || reply == nil {
		t.Fatalf("plan %+v, reply %+v", plan, reply)
	}
	if plan.Kind != event.OriginTurn || plan.Tainted {
		t.Errorf("plan origin %+v: want a clean turn", *plan)
	}
	if reply.Kind != event.OriginTurn || !reply.Tainted || reply.Session != plan.Session || reply.Session == "" {
		t.Errorf("reply origin %+v (plan %+v): want a tainted turn in the same session", *reply, *plan)
	}
}

// R-DLV-14: a task run's report is a task's, in the task conversation's session.
func TestTaskReportCarriesItsOrigin(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	j := rates()
	e.jobs[j.ID] = j
	e.states.m["task:j1"], _ = json.Marshal(State{V: StateVersion, Session: "ts", Started: t0, Last: t0, Tainted: true})
	e.smart.push(say("USD 41.2"))
	out := e.turn(t, fired(j))
	want := event.Origin{Kind: event.OriginTask, Session: "ts", Tainted: true}
	if got := originOf(t, out, "USD 41.2"); got != want {
		t.Errorf("origin %+v, want %+v", got, want)
	}
}

// R-DLV-14: a reminder is clean even in a chat session that holds outside content.
func TestReminderCarriesACleanOrigin(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	r := schedule.Job{ID: "r1", Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindReminder,
		Cron: "0 9 * * *", Text: "call mum", Created: t0}
	e.jobs[r.ID] = r
	e.states.m["telegram:42"], _ = json.Marshal(State{V: StateVersion, Session: "cs", Started: t0, Last: t0, Tainted: true,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "x"}}})
	out := e.turn(t, fired(r))
	want := event.Origin{Kind: event.OriginReminder, Session: "cs", Job: "r1"} // a reply names it (ADR-0091)
	if got := originOf(t, out, "call mum"); got != want {
		t.Errorf("origin %+v, want %+v", got, want)
	}
}

// R-DLV-14: a system job's report is tainted, as it reports on memory outside content
// may have reached.
func TestSystemReportCarriesATaintedOrigin(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.SystemJobs = map[string]SystemJob{builtin.AuditJobID: func(context.Context, texts.Speaker) (string, error) { return "audit", nil }}
	e.a = New(overDefaults(Config{}), e.deps)
	j := schedule.Job{ID: builtin.AuditJobID, Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindSystem,
		Cron: "0 4 * * *", Text: "Memory audit", System: true, Created: t0}
	e.jobs[j.ID] = j
	out := e.turn(t, fired(j))
	got := originOf(t, out, "audit")
	if got.Kind != event.OriginSystem || !got.Tainted {
		t.Errorf("origin %+v, want a tainted system one", got)
	}
}
