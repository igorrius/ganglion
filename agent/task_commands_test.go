package agent

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
)

// pruneTask is a weekly task whose grant lists one Docker command that needs
// a press, docker image prune -af, in the home folder.
func pruneTask() schedule.Job {
	return schedule.Job{ID: "j1", Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindTask,
		Cron: "0 3 * * 0", Text: "prune the unused Docker images",
		Grant: tool.Grant{Tools: []string{builtin.HostExecTool}, ReplyTo: "telegram:42",
			Commands: []tool.Command{{Argv: []string{"docker", "image", "prune", "-af"}}}}, Created: t0}
}

// newTaskCommandsEnv is a confirmEnv with host.exec over a runner that only
// records, and the real policy with its alerts to the admins.
func newTaskCommandsEnv(t *testing.T) (*confirmEnv, *ranCommands) {
	t.Helper()
	ran := &ranCommands{}
	e := newConfirmEnv(t, builtin.NewHostExec(ran, planNoIgnored{}, planGate{}, "/home/o"))
	e.deps.Policy = policy.New(policy.DefaultConfig(), e.deps.Tools, e.pending, policy.WithAlerts(e.deps.Alerts),
		policy.WithClock(func() time.Time { return e.now }, time.UTC)).Middleware()
	e.a = New(overDefaults(Config{}), e.deps)
	return e, ran
}

// runPrune fires the task once: the run calls its granted command, then
// docker rm x, which the grant does not list, and answers. It returns the
// run's event and the grant alerts it sent.
func runPrune(t *testing.T, e *confirmEnv) (event.Event, []event.Outgoing) {
	t.Helper()
	e.smart.push(callWith(builtin.HostExecTool, `{"argv":["docker","image","prune","-af"],"reason":"weekly"}`),
		callWith(builtin.HostExecTool, `{"argv":["docker","rm","x"],"reason":"and this one"}`), say("Pruned."))
	ev := fired(e.jobs["j1"])
	out := e.turn(t, ev)
	if _, asked := findOut(out, func(o event.Outgoing) bool { return len(o.Buttons) > 0 }); asked {
		t.Fatalf("a question in a task's run: %+v", out)
	}
	var alerts []event.Outgoing
	for _, o := range out {
		if strings.HasPrefix(o.IdemKey, "alert:grant:j1:") {
			alerts = append(alerts, o)
		}
	}
	return ev, alerts
}

// design §10 check 7, R-POL-8, ADR-0040: a weekly task runs the command its
// grant lists with no press; one it does not list is refused as out of
// grant, with one notice to the admin that names it, and a second run the
// same day sends the same notice, which the outbox keeps once.
func TestATaskRunsItsGrantedCommandAndOnlyThat(t *testing.T) {
	e, ran := newTaskCommandsEnv(t)
	e.jobs["j1"] = pruneTask()
	ev, alerts := runPrune(t, e)
	if !slices.EqualFunc(ran.argv, [][]string{{"docker", "image", "prune", "-af"}}, slices.Equal) {
		t.Fatalf("ran %q, want only the granted command", ran.argv)
	}
	if d := e.decisions(t, ev.ID+":1"); !slices.Equal(d, []journal.Decision{journal.DecisionAllowed}) {
		t.Fatalf("the granted command: decisions %v", d)
	}
	if d := e.decisions(t, ev.ID+":2"); !slices.Equal(d, []journal.Decision{journal.DecisionOutOfGrant}) {
		t.Fatalf("docker rm x: decisions %v", d)
	}
	if len(alerts) != 1 || alerts[0].Conv != "telegram:1" || !strings.Contains(alerts[0].Text, "docker rm x") {
		t.Fatalf("grant alerts %+v, want one to the admin naming docker rm x", alerts)
	}

	e.now = e.now.Add(time.Hour)
	_, again := runPrune(t, e)
	if len(again) != 1 || again[0].IdemKey != alerts[0].IdemKey {
		t.Fatalf("the second run's alerts %+v, want the first's key %s, which the outbox keeps once", again, alerts[0].IdemKey)
	}
	if len(ran.argv) != 2 {
		t.Fatalf("ran %q over two runs", ran.argv)
	}
}
