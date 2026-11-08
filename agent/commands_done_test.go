package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
)

// fakeCommands are host commands' rows in memory: Running lists the keys
// whose job reads running.
type fakeCommands struct{ jobs map[string]host.Job }

func (f *fakeCommands) Get(_ context.Context, key string) (host.Job, error) {
	j, ok := f.jobs[key]
	if !ok {
		return host.Job{}, host.ErrNoJob
	}
	return j, nil
}

func (f *fakeCommands) Running(_ context.Context, keys []string) ([]string, error) {
	var out []string
	for _, k := range keys {
		if f.jobs[k].State == host.JobRunning {
			out = append(out, k)
		}
	}
	return out, nil
}

// withCommands gives e's agent the commands f.
func (e *env) withCommands(f *fakeCommands) {
	e.deps.Commands = f
	e.a = New(overDefaults(Config{}), e.deps)
}

// commandDoneEvent is the event the runner puts when command key ends in conv.
func commandDoneEvent(conv, key string) event.Event {
	seq++
	p, _ := json.Marshal(event.CommandDone{Key: key})
	return event.Event{ID: fmt.Sprintf("01CD%04d", seq), IdemKey: "command:" + key + ":done", Source: "telegram",
		Conv: conv, Kind: event.KindCommandDone, Payload: p, At: t0}
}

// setState stores st as conv's state.
func (e *env) setState(conv string, st State) {
	st.V = StateVersion
	e.states.m[conv], _ = json.Marshal(st)
}

// R-CMD-4: the turn of event e1 called host.exec, which went to the
// background as "e1:1"; the state lists it. Its command.done resumes the
// conversation: the outcome is outside data, and the model goes on with its
// tools.
func TestACommandThatEndsResumesItsConversation(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	cmds := &fakeCommands{jobs: map[string]host.Job{"e1:1": {Key: "e1:1", Conv: "telegram:42", Transport: "telegram",
		Principal: owner, Argv: []string{"du", "-sh", "/home/o/project"}, State: host.JobDone, Background: true,
		Text: "exit 0 after 41s\n2.1G\t/home/o/project"}}}
	e.withCommands(cmds)
	e.setState("telegram:42", State{Session: "s", Started: t0, Last: t0, Commands: []string{"e1:1"},
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Igor: how big is my project?"}, {Role: provider.RoleAssistant, Content: "It runs."}}})
	e.smart.push(say("The tree: ~/project is 2.1 GiB."))
	ev := commandDoneEvent("telegram:42", "e1:1")
	out := e.turn(t, ev)
	reply, ok := findOut(out, isReply)
	if !ok || !strings.Contains(reply.Text, "2.1 GiB") || reply.Conv != "telegram:42" || reply.Transport != "telegram" {
		t.Fatalf("no answer in the conversation: %+v", out)
	}
	st := e.state(t)
	if len(st.Commands) != 0 || !st.Tainted {
		t.Fatalf("state = %+v; the job is not listed any more and the output tainted the session", st)
	}
	reqs := e.smart.requests()
	last := lastUser(reqs[len(reqs)-1])
	if !strings.Contains(last, "[command finished] du -sh /home/o/project") || !strings.Contains(last, `<external source="host.exec">`) ||
		!strings.Contains(last, "2.1G") {
		t.Fatalf("the model did not read the outcome as outside data:\n%s", last)
	}
	if len(reqs[len(reqs)-1].Tools) == 0 {
		t.Fatal("a resumed command turn offers no tools; the plan could not go on")
	}
	if tr, err := e.turns.Get(context.Background(), ev.ID); err != nil || tr.Principal != owner {
		t.Fatalf("turn %+v, %v", tr, err)
	}
}

// The resumed turn's calls run one level deeper than the call that started
// the command (R-AGT-4).
func TestACommandTurnRunsOneDeeper(t *testing.T) {
	clock := newClock()
	e := newEnv(t, Config{}, registry(t, clock))
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"e1:1": {Key: "e1:1", Conv: "telegram:42", Principal: owner,
		Depth: 1, Argv: []string{"du"}, State: host.JobDone, Background: true, Text: "exit 0"}}})
	e.setState("telegram:42", State{Session: "s", Started: t0, Last: t0, Commands: []string{"e1:1"}})
	e.smart.push(callTool("clock"), say("done"))
	e.turn(t, commandDoneEvent("telegram:42", "e1:1"))
	if len(clock.calls) != 1 || clock.calls[0].Depth != 2 || !clock.calls[0].Tainted {
		t.Fatalf("calls %+v", clock.calls)
	}
}

// A command the session no longer waits for (/new came in between) gets a
// fixed line and no model turn, and its output stays out of the new session.
func TestACommandThatEndsAfterTheSessionMovedOnSaysSoWithoutAModel(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"e1:1": {Key: "e1:1", Conv: "telegram:42", Principal: owner,
		Argv: []string{"du", "-sh", "/x"}, State: host.JobDone, Background: true, Text: "exit 0"}}})
	out := e.turn(t, commandDoneEvent("telegram:42", "e1:1")) // state.Commands is empty
	reply, ok := findOut(out, isReply)
	if !ok || reply.Text != "The command `du -sh /x` finished after this conversation moved on." {
		t.Fatalf("out = %+v", out)
	}
	if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
		t.Fatalf("%d model calls", n)
	}
	if st := e.state(t); st.Tainted || len(st.Messages) != 0 {
		t.Fatalf("a late command's output entered the new session: %+v", st)
	}
}

// A backtick in the command cannot end the code span of the late line early.
func TestTheLateLineShowsABacktickInsideItsSpan(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"e1:1": {Key: "e1:1", Conv: "telegram:42", Principal: owner,
		Argv: []string{"ls", "a`b"}, State: host.JobDone, Background: true, Text: "exit 0"}}})
	out := e.turn(t, commandDoneEvent("telegram:42", "e1:1"))
	if reply, ok := findOut(out, isReply); !ok || !strings.Contains(reply.Text, "`` ls 'a`b' ``") {
		t.Fatalf("out = %+v", out)
	}
}

// Abuse: a command.done naming another conversation's command does nothing
// here: neither its line nor its output reaches this conversation.
func TestACommandOfAnotherConversationDoesNothing(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"e1:1": {Key: "e1:1", Conv: "telegram:7", Principal: owner,
		Argv: []string{"cat", "/home/o/.secret"}, State: host.JobDone, Background: true, Text: "exit 0\nhunter2"}}})
	e.setState("telegram:42", State{Session: "s", Started: t0, Last: t0, Commands: []string{"e1:1"}})
	out := e.turn(t, commandDoneEvent("telegram:42", "e1:1"))
	if len(out) != 0 || len(e.smart.requests()) != 0 {
		t.Fatalf("out = %+v", out)
	}
	if st := e.state(t); st.Tainted || len(st.Messages) != 0 {
		t.Fatalf("state %+v", st)
	}
}

// The end of a command the runner does not know does nothing.
func TestTheEndOfAnUnknownCommandDoesNothing(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{}})
	if out := e.turn(t, commandDoneEvent("telegram:42", "gone")); len(out) != 0 || len(e.smart.requests()) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

// Ruling 7 of the M3d plan: a command of a scheduled task resumes as a turn
// of that task, under its grant, answering to the grant's reply target.
func TestACommandInATaskResumesUnderTheTaskGrant(t *testing.T) {
	clock := newClock()
	e := newConfirmEnv(t, clock)
	j := rates()
	j.Grant.Tools = []string{"clock", "host.exec"}
	e.jobs[j.ID] = j
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"r1:1": {Key: "r1:1", Conv: "task:j1", Transport: "telegram",
		Principal: owner, Argv: []string{"du", "-sh", "/srv"}, State: host.JobDone, Background: true, Text: "exit 0\n9G\t/srv"}}})
	e.setState("task:j1", State{Session: "s", Started: t0, Last: t0, Commands: []string{"r1:1"}})
	e.smart.push(callTool("clock"), say("/srv holds 9 GiB."))
	ev := commandDoneEvent("task:j1", "r1:1")
	out := e.turn(t, ev)
	if len(clock.calls) != 1 {
		t.Fatalf("clock ran %d times", len(clock.calls))
	}
	c := clock.calls[0]
	if c.Grant == nil || !slices.Equal(c.Grant.Tools, j.Grant.Tools) || c.Principal != owner || c.Conv != "task:j1" || !c.Admin || !c.Tainted {
		t.Fatalf("call %+v", c)
	}
	reply, ok := findOut(out, isReply)
	if !ok || reply.Text != "/srv holds 9 GiB." || reply.Conv != j.Grant.ReplyTo || reply.Transport != "telegram" ||
		reply.Origin == nil || reply.Origin.Kind != event.OriginTask {
		t.Fatalf("reply %+v in %+v", reply, out)
	}
	reqs := e.smart.requests()
	if got := lastUserRaw(reqs[0]); !strings.HasPrefix(got, "[command finished] du -sh /srv: ") ||
		strings.Count(got, "[unattended scheduled run:") != 1 {
		t.Fatalf("the task's command turn read %q", got)
	}
	for _, tl := range reqs[0].Tools {
		if !slices.Contains(j.Grant.Tools, tl.Name) {
			t.Fatalf("a tool outside the grant was offered: %s", tl.Name)
		}
	}
	if st := e.stateOf(t, "task:j1"); len(st.Commands) != 0 {
		t.Fatalf("task state %+v", st)
	}
	if len(e.state(t).Messages) != 0 {
		t.Fatal("the task's command turn went into the chat's history")
	}
}

// A command of a task deleted since does nothing.
func TestACommandOfADeletedTaskDoesNothing(t *testing.T) {
	e := newConfirmEnv(t, newClock())
	j := rates()
	j.Deleted = true
	e.jobs[j.ID] = j
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"r1:1": {Key: "r1:1", Conv: "task:j1", Principal: owner,
		Argv: []string{"du"}, State: host.JobDone, Background: true, Text: "exit 0"}}})
	e.setState("task:j1", State{Session: "s", Started: t0, Last: t0, Commands: []string{"r1:1"}})
	if out := e.turn(t, commandDoneEvent("task:j1", "r1:1")); len(out) != 0 || len(e.smart.requests()) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

// A listed command the runner no longer reports (its row is gone, or it
// ended without the background) is dropped at the next turn, and the plan is
// not kept for it.
func TestStaleCommandsAreDropped(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	f := &planFinish{}
	e.deps.Plans = f
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{"quick": {Key: "quick", State: host.JobDone}}})
	e.setState("telegram:42", State{Session: "s", Started: t0, Last: t0, Commands: []string{"gone", "quick"}})
	e.smart.push(say("hi"))
	e.turn(t, message(owner, "Igor", "hello"))
	if st := e.state(t); len(st.Commands) != 0 {
		t.Fatalf("commands %v", st.Commands)
	}
	if !slices.Equal(f.kept, []bool{false}) {
		t.Fatalf("kept %v", f.kept)
	}
}

// A command still running, and one that ended in the background whose event
// has not been handled yet, stay listed: the event still resumes them.
func TestCommandsStillToBeToldStayListed(t *testing.T) {
	e := newEnv(t, Config{}, registry(t, newClock()))
	f := &planFinish{}
	e.deps.Plans = f
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{
		"run":  {Key: "run", State: host.JobRunning, Background: true},
		"told": {Key: "told", State: host.JobDone, Background: true},
		"lost": {Key: "lost", State: host.JobInterrupted, Background: true},
	}})
	e.setState("telegram:42", State{Session: "s", Started: t0, Last: t0, Commands: []string{"run", "told", "lost"}})
	e.smart.push(say("still on it"))
	e.turn(t, message(owner, "Igor", "how is it going?"))
	if st := e.state(t); !slices.Equal(st.Commands, []string{"run", "told", "lost"}) {
		t.Fatalf("commands %v", st.Commands)
	}
	if !slices.Equal(f.kept, []bool{true}) {
		t.Fatalf("kept %v", f.kept)
	}
}

// A task's command that failed every attempt has nobody to apologise to in
// its conversation: only the admins hear of it. A person's gets the apology.
func TestGiveUpOnATaskCommandOnlyAlerts(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	out := e.a.GiveUp(commandDoneEvent("task:j1", "r1:1"), 5)
	if len(out) != 1 || out[0].Conv != "telegram:1" || !strings.Contains(out[0].Text, "task:j1") {
		t.Fatalf("out %+v", out)
	}
	out = e.a.GiveUp(commandDoneEvent("telegram:42", "e1:1"), 5)
	if len(out) != 2 || out[0].Conv != "telegram:42" || out[0].Text != replyGiveUp {
		t.Fatalf("out %+v", out)
	}
}

// /resume brings a session back with only the commands still running: one
// that ended while it was archived was told as late, and nothing resumes it.
func TestResumeKeepsOnlyRunningCommands(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.withCommands(&fakeCommands{jobs: map[string]host.Job{
		"run":   {Key: "run", State: host.JobRunning, Background: true},
		"ended": {Key: "ended", State: host.JobDone, Background: true},
	}})
	old := State{V: StateVersion, Session: "A", Started: t0, Last: t0, Commands: []string{"run", "ended"},
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Igor: clean up"}, {Role: provider.RoleAssistant, Content: "It runs."}}}
	if err := e.archive.Put(context.Background(), "telegram:42", old, "new"); err != nil {
		t.Fatal(err)
	}
	e.turn(t, message(owner, "Igor", "/resume 1"))
	if st := e.state(t); st.Session != "A" || !slices.Equal(st.Commands, []string{"run"}) {
		t.Fatalf("state %+v", st)
	}
}

// approvedJob is a host command whose approved run goes on in the background.
type approvedJob struct{ sideTool }

func (o *approvedJob) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	o.calls = append(o.calls, c)
	return tool.Result{Text: "still running", External: true, Job: c.Key()}, nil
}

// A command an approved press starts that goes to the background is listed,
// so its end resumes the conversation; a rerun of the press, which reads the
// stored outcome, lists it once.
func TestAnApprovedCommandInTheBackgroundIsListed(t *testing.T) {
	o := &approvedJob{name: "job.start", class: tool.ClassSideEffect}
	e := newConfirmEnv(t, o)
	q, ev := e.ask(t, "job.start")
	pr := press(owner, q.Buttons[0].ID, "77")
	e.smart.push(say("It runs."), say("It runs."))
	if _, _, err := e.a.Handle(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	e.turn(t, pr)
	if len(o.calls) != 1 {
		t.Fatalf("the call ran %d times", len(o.calls))
	}
	if st := e.state(t); !slices.Equal(st.Commands, []string{ev.ID + ":1"}) || len(st.Pending) != 0 {
		t.Fatalf("state %+v", st)
	}
}
