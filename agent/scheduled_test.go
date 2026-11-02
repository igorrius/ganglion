package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/turns"
)

// jobs is the job store in memory.
type jobs map[string]schedule.Job

func (j jobs) Get(_ context.Context, id string) (schedule.Job, error) {
	job, ok := j[id]
	if !ok {
		return schedule.Job{}, fmt.Errorf("job %s: %w", id, schedule.ErrNotFound)
	}
	return job, nil
}

func rates() schedule.Job {
	return schedule.Job{ID: "j1", Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindTask,
		Cron: "0 * * * *", Text: "send me the exchange rates, compared with the previous runs",
		Grant: tool.Grant{Tools: []string{"clock"}, Hosts: []string{"bank.gov.ua"}, ReplyTo: "telegram:42"}, Created: t0}
}

// fired is the event the scheduler puts for job j.
func fired(j schedule.Job) event.Event {
	seq++
	p, _ := json.Marshal(event.Schedule{Job: j.ID, Scheduled: t0.UnixMilli()})
	return event.Event{ID: fmt.Sprintf("01SC%04d", seq), IdemKey: fmt.Sprintf("cron:%s:%d", j.ID, seq), Source: schedule.Source,
		Conv: j.EventConv(), Kind: event.KindSchedule, Payload: p, At: t0}
}

func (e *env) stateOf(t *testing.T, conv string) State {
	t.Helper()
	st, err := DecodeState(e.states.m[conv])
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReminderNeedsNoModel(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	r := schedule.Job{ID: "r1", Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindReminder,
		Cron: "0 9 * * *", Text: "call mum", Created: t0}
	e.jobs[r.ID] = r
	ev := fired(r)
	out := e.turn(t, ev)
	if len(out) != 1 || out[0].Text != "call mum" || out[0].Transport != "telegram" || out[0].Conv != "telegram:42" ||
		out[0].IdemKey != ev.IdemKey+":reply:0" || len(out[0].Buttons) != 0 {
		t.Fatalf("out %+v", out)
	}
	if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
		t.Fatalf("%d model calls for a reminder", n)
	}
	if st := e.state(t); len(st.Messages) != 0 {
		t.Fatalf("the reminder changed the chat's history: %+v", st.Messages)
	}
	tr, err := e.turns.Get(context.Background(), ev.ID)
	if err != nil || tr.Principal != owner || tr.Outcome != turns.OutcomeCommand {
		t.Fatalf("turn %+v, %v", tr, err)
	}
}

// A task's turn runs as its owner in its own conversation, with the
// grant on every call: a call inside it runs without a question though
// the session is tainted, one outside it is refused without a question,
// and the answer goes to the grant's reply target.
func TestTaskTurnUsesGrant(t *testing.T) {
	clock := newClock()
	page := &sideTool{name: "web.fetch", result: tool.Result{Text: "page", External: true}, class: tool.ClassExternal}
	e := newConfirmEnv(t, clock, page)
	j := rates()
	e.jobs[j.ID] = j
	e.states.m["task:j1"], _ = json.Marshal(State{V: StateVersion, Session: "s", Started: t0, Last: t0, Tainted: true})
	e.smart.push(callTools("clock", "web.fetch"), say("USD 41.2"))
	ev := fired(j)
	out := e.turn(t, ev)
	if len(clock.calls) != 1 || len(page.calls) != 0 {
		t.Fatalf("clock ran %d, web.fetch ran %d", len(clock.calls), len(page.calls))
	}
	c := clock.calls[0]
	if c.Grant == nil || !slices.Equal(c.Grant.Tools, j.Grant.Tools) || c.Principal != owner || c.Conv != "task:j1" ||
		c.Transport != "telegram" || !c.Admin || !c.Tainted {
		t.Fatalf("call %+v", c)
	}
	if _, asked := findOut(out, func(o event.Outgoing) bool { return len(o.Buttons) > 0 }); asked {
		t.Fatalf("a question in a task's turn: %+v", out)
	}
	reply, ok := findOut(out, isReply)
	if !ok || reply.Text != "USD 41.2" || reply.Conv != "telegram:42" || reply.Transport != "telegram" || reply.IdemKey != ev.IdemKey+":reply:0" {
		t.Fatalf("reply %+v in %+v", reply, out)
	}
	if d := e.decisions(t, ev.ID+":2"); !slices.Equal(d, []journal.Decision{journal.DecisionOutOfGrant}) {
		t.Fatalf("web.fetch decisions %v", d)
	}
	st := e.stateOf(t, "task:j1")
	if len(st.Messages) < 2 || st.Messages[0].Content != "Scheduled task: "+j.Text || len(st.Pending) != 0 {
		t.Fatalf("task history %+v", st)
	}
	// The next run sees the previous one in its history.
	e.smart.push(say("USD 41.3, up 0.1"))
	e.turn(t, fired(j))
	reqs := e.smart.requests()
	var seen []string
	for _, m := range reqs[len(reqs)-1].Messages {
		seen = append(seen, m.Content)
	}
	if !slices.Contains(seen, "USD 41.2") {
		t.Fatalf("the second run does not see the first: %q", seen)
	}
	if len(e.state(t).Messages) != 0 {
		t.Fatal("the task's turns went into the chat's history")
	}
}

// A task of an owner who is no longer an admin gets no tools.
func TestDemotedOwnerTaskHasNoTools(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	e.admin = false
	j := rates()
	e.jobs[j.ID] = j
	e.turn(t, fired(j))
	reqs := e.smart.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Fatalf("requests %+v", reqs)
	}
}

// A job deleted after its event was put, or an event that does not fit
// its job, does nothing.
func TestDeletedJobIsSkipped(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	deleted := rates()
	deleted.Deleted = true
	e.jobs[deleted.ID] = deleted
	moved := rates()
	moved.ID = "j2"
	e.jobs[moved.ID] = moved
	wrongConv := fired(moved)
	wrongConv.Conv = "telegram:42"
	forged := fired(moved)
	forged.Source = "telegram"
	for name, ev := range map[string]event.Event{
		"deleted":                fired(deleted),
		"unknown":                fired(schedule.Job{ID: "gone", Kind: schedule.KindTask}),
		"wrong conv":             wrongConv,
		"not from the scheduler": forged,
	} {
		out := e.turn(t, ev)
		if len(out) != 0 || len(e.smart.requests()) != 0 {
			t.Fatalf("%s: out %+v", name, out)
		}
		if tr, err := e.turns.Get(context.Background(), ev.ID); err != nil || tr.Outcome != turns.OutcomeCommand {
			t.Fatalf("%s: turn %+v, %v", name, tr, err)
		}
	}
}

// A task's instruction is not a command, and its session is not cut by
// silence: the earlier runs stay its history.
func TestTaskIsNeitherCommandNorIdle(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	j := rates()
	j.Text = "/new"
	e.jobs[j.ID] = j
	old := State{V: StateVersion, Session: "s", Started: t0.AddDate(0, 0, -7), Last: t0.AddDate(0, 0, -1),
		Messages: []provider.Message{{Role: "user", Content: "Scheduled task: earlier"}, {Role: "assistant", Content: "USD 41.0"}}}
	e.states.m["task:j1"], _ = json.Marshal(old)
	e.turn(t, fired(j))
	st := e.stateOf(t, "task:j1")
	if st.Session != "s" || len(st.Messages) != 4 || st.Messages[2].Content != "Scheduled task: /new" {
		t.Fatalf("state %+v", st)
	}
}

func TestGiveUpOnAScheduledEventOnlyAlerts(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	out := e.a.GiveUp(fired(rates()), 5)
	if len(out) != 1 || out[0].Conv != "telegram:1" || !strings.Contains(out[0].Text, "task:j1") {
		t.Fatalf("out %+v", out)
	}
}
