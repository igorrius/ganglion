package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

type fakeStats struct{}

func (fakeStats) Read(context.Context) host.Report {
	return host.Report{Machine: host.Machine{MemTotal: 1 << 30, MemAvailable: 1 << 29, OK: true}}
}

func TestHostStatsSpec(t *testing.T) {
	s := NewHostStats(fakeStats{}).Spec()
	if s.Name != HostStatsTool || s.Class != tool.ClassRead || s.Policy != tool.PolicyAuto || !s.AdminOnly || !s.LocalSafe {
		t.Fatalf("spec %+v", s)
	}
}

func TestHostStatsRunIsNotExternal(t *testing.T) {
	h := NewHostStats(fakeStats{})
	res, err := h.Run(t.Context(), tool.Call{Name: HostStatsTool, Args: json.RawMessage(`{}`)})
	if err != nil || res.External || !strings.Contains(res.Text, "Memory:") {
		t.Fatalf("result %+v, %v", res, err)
	}
}

func TestHostStatsTakesNoArguments(t *testing.T) {
	s := NewHostStats(fakeStats{}).Spec()
	if err := tool.CheckArgs(s, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("an empty call: %v", err)
	}
	if err := tool.CheckArgs(s, json.RawMessage(`{"path":"/"}`)); !errors.Is(err, tool.ErrBadArgs) {
		t.Fatalf("a call with an argument: %v, want ErrBadArgs", err)
	}
}

type commands struct {
	got    []host.Request
	answer host.Answer
}

func (c *commands) Exec(_ context.Context, q host.Request) (host.Answer, error) {
	c.got = append(c.got, q)
	return c.answer, nil
}

type noIgnored struct{}

func (noIgnored) Ignored(context.Context, string, []string) (string, bool) { return "", false }

// ignoredAll reports every file ignored in repo.
type ignoredAll struct{ repo string }

func (g ignoredAll) Ignored(context.Context, string, []string) (string, bool) { return g.repo, true }

type open bool

func (o open) Open() bool { return bool(o) }

func execArgs(argv ...string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"argv": argv, "reason": "r"})
	return b
}

func TestHostExecSpec(t *testing.T) {
	s := NewHostExec(&commands{}, noIgnored{}, open(true), "/home/o").Spec()
	if s.Name != HostExecTool || s.Class != tool.ClassSideEffect || s.Policy != tool.PolicyConfirm || !s.AdminOnly || s.LocalSafe {
		t.Fatalf("spec %+v", s)
	}
	if !strings.Contains(s.Description, "/home/o") {
		t.Fatalf("the description does not name home: %s", s.Description)
	}
	if err := tool.CheckArgs(s, json.RawMessage(`{"argv":["df"],"reason":"r","shell":true}`)); !errors.Is(err, tool.ErrBadArgs) {
		t.Fatalf("an undeclared argument: %v, want ErrBadArgs", err)
	}
}

func TestHostExecRunsAReadCommandAsTheCallSays(t *testing.T) {
	cs := &commands{answer: host.Answer{Done: true, Job: host.Job{Text: "exit 0 after 1ms\n/ 10G"}}}
	h := NewHostExec(cs, noIgnored{}, open(true), "/home/o")
	res, err := h.Run(context.Background(), tool.Call{TurnID: "e1", N: 2, Conv: "telegram:1", Transport: "telegram",
		Principal: "p", Depth: 1, Args: json.RawMessage(`{"argv":["df","-h"],"reason":"space","timeout":30}`)})
	if err != nil || !res.External || res.Job != "" || !strings.Contains(res.Text, "/ 10G") {
		t.Fatalf("result = %+v, %v", res, err)
	}
	q := cs.got[0]
	if q.Key != "e1:2" || q.Dir != "/home/o" || q.Depth != 1 || q.Conv != "telegram:1" || q.Transport != "telegram" ||
		q.Principal != "p" || q.Timeout != 30*time.Second || strings.Join(q.Argv, " ") != "df -h" {
		t.Fatalf("request = %+v", q)
	}
}

func TestHostExecRunsInTheGivenFolder(t *testing.T) {
	cs := &commands{answer: host.Answer{Done: true}}
	if _, err := NewHostExec(cs, noIgnored{}, open(true), "/home/o").Run(context.Background(),
		tool.Call{Args: json.RawMessage(`{"argv":["ls"],"dir":"/srv","reason":"r"}`)}); err != nil {
		t.Fatal(err)
	}
	if cs.got[0].Dir != "/srv" || cs.got[0].Timeout != 0 {
		t.Fatalf("request = %+v", cs.got[0])
	}
}

func TestHostExecInTheBackgroundSaysItRuns(t *testing.T) {
	cs := &commands{answer: host.Answer{Job: host.Job{Key: "e1:2", State: host.JobRunning}}}
	res, err := NewHostExec(cs, noIgnored{}, open(true), "/h").Run(context.Background(),
		tool.Call{TurnID: "e1", N: 2, Args: json.RawMessage(`{"argv":["du","-sh","/h"],"reason":"r"}`)})
	if err != nil || res.Job != "e1:2" || !strings.Contains(res.Text, "background") || !res.External {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// ticks records the plan ticks host.exec sends.
type ticks struct {
	got []string
	err error
}

func (k *ticks) Ran(_ context.Context, conv string, cmd tool.Command, ok bool) error {
	k.got = append(k.got, fmt.Sprintf("%s %s in %q ok=%v", conv, strings.Join(cmd.Argv, " "), cmd.Dir, ok))
	return k.err
}

// #38: a command that ends here ticks its plan's step, done or failed by its
// exit, with argv and folder as called; one gone to the background does not
// tick yet, and a failed tick leaves the answer as it is.
func TestHostExecTicksThePlanWhenTheCommandEnds(t *testing.T) {
	for _, c := range []struct {
		answer host.Answer
		want   []string
	}{
		{host.Answer{Done: true, Job: host.Job{State: host.JobDone, Text: "exit 0 after 1ms\n"}}, []string{`telegram:1 rm -r /var/tmp/x in "/srv" ok=true`}},
		{host.Answer{Done: true, Job: host.Job{State: host.JobDone, Text: "exit 1 after 1ms\n"}}, []string{`telegram:1 rm -r /var/tmp/x in "/srv" ok=false`}},
		{host.Answer{Job: host.Job{Key: "e1:2", State: host.JobRunning}}, nil},
	} {
		k := &ticks{err: errors.New("db gone")}
		h := NewHostExec(&commands{answer: c.answer}, noIgnored{}, open(true), "/h", WithPlanTicks(k, slog.New(slog.DiscardHandler)))
		res, err := h.Run(context.Background(), tool.Call{TurnID: "e1", N: 2, Conv: "telegram:1", Approved: true,
			Args: json.RawMessage(`{"argv":["rm","-r","/var/tmp/x"],"dir":"/srv","reason":"r"}`)})
		if err != nil || res.Text == "" {
			t.Fatalf("result = %+v, %v; a failed tick must not change the answer", res, err)
		}
		if !slices.Equal(k.got, c.want) {
			t.Errorf("ticks %q, want %q", k.got, c.want)
		}
	}
}

// Review Focus 1 and 2: the tool checks again what the policy decided, so a
// path around the policy runs nothing it would not: forbidden never, even
// pressed or planned; a command that needs a press only pressed, planned or
// granted.
func TestHostExecRefusesWhatThePolicyShouldHaveStopped(t *testing.T) {
	cs := &commands{answer: host.Answer{Done: true}}
	h := NewHostExec(cs, noIgnored{}, open(true), "/h")
	all := func(tool.Command) bool { return true }
	refused := []tool.Call{
		{Args: execArgs("sudo", "true")},                                                          // forbidden
		{Args: execArgs("sudo", "true"), Approved: true},                                          // forbidden though pressed
		{Args: execArgs("sudo", "true"), Planned: all},                                            // forbidden though planned
		{Args: execArgs("rm", "-r", "/tmp/x")},                                                    // needs a press; neither approved nor planned
		{Args: execArgs("cat", "/etc/shadow"), Tainted: true},                                     // content, tainted, unpressed
		{Args: execArgs("bash", "-c", "true"), Planned: func(tool.Command) bool { return false }}, // not in the plan
		{Args: execArgs("rm", "-r", "/tmp/x"), Grant: &tool.Grant{Tools: []string{HostExecTool}}}, // not in the grant
		{Args: json.RawMessage(`{"argv":[],"reason":"r"}`), Approved: true},                       // no program
	}
	for _, c := range refused {
		if _, err := h.Run(context.Background(), c); err == nil {
			t.Errorf("%s (approved %v) ran", c.Args, c.Approved)
		}
	}
	if len(cs.got) != 0 {
		t.Fatalf("the runner was asked %d times", len(cs.got))
	}
	if _, err := h.Run(context.Background(), refused[0]); !errors.Is(err, tool.ErrForbidden) {
		t.Fatalf("sudo: %v, want ErrForbidden", err)
	}
	if _, err := h.Run(context.Background(), refused[6]); !errors.Is(err, tool.ErrOutOfGrant) {
		t.Fatalf("a task's change: %v, want ErrOutOfGrant", err)
	}
	ran := []tool.Call{
		{Approved: true, Args: execArgs("rm", "-r", "/tmp/x")},
		{Planned: all, Args: execArgs("rm", "-r", "/tmp/x")},
		{Grant: &tool.Grant{Tools: []string{HostExecTool}}, Args: execArgs("df", "-h")},
		{Grant: &tool.Grant{Tools: []string{HostExecTool}, Commands: []tool.Command{{Argv: []string{"rm", "-r", "/tmp/x"}}}}, Args: execArgs("rm", "-r", "/tmp/x")},
	}
	for _, c := range ran {
		if _, err := h.Run(context.Background(), c); err != nil {
			t.Errorf("%s was refused: %v", c.Args, err)
		}
	}
}

// A grant lists argv and folder exactly: the same argv in another folder, or with
// one more argument, is outside it, and forbidden stays forbidden though listed.
func TestHostExecGrantIsExact(t *testing.T) {
	cs := &commands{answer: host.Answer{Done: true}}
	h := NewHostExec(cs, noIgnored{}, open(true), "/h")
	g := &tool.Grant{Tools: []string{HostExecTool}, Commands: []tool.Command{{Argv: []string{"rm", "-r", "build"}, Dir: "/srv/a"}, {Argv: []string{"sudo", "true"}}}}
	args := func(dir string, argv ...string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"argv": argv, "dir": dir, "reason": "r"})
		return b
	}
	for _, c := range []struct {
		args json.RawMessage
		want error
	}{
		{args("/srv/a", "rm", "-r", "build"), nil},
		{args("/srv/b", "rm", "-r", "build"), tool.ErrOutOfGrant},
		{args("", "rm", "-r", "build"), tool.ErrOutOfGrant},
		{args("/srv/a", "rm", "-r", "build", "x"), tool.ErrOutOfGrant},
		{args("", "sudo", "true"), tool.ErrForbidden},
	} {
		if _, err := h.Run(context.Background(), tool.Call{Grant: g, Args: c.args}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.args, err, c.want)
		}
	}
}

// A command without dir runs in home, so a grant without a folder covers the
// call with dir set to home, and the reverse; another folder stays outside.
func TestHostExecGrantReadsNoFolderAsHome(t *testing.T) {
	h := NewHostExec(&commands{answer: host.Answer{Done: true}}, noIgnored{}, open(true), "/h")
	for _, c := range []struct {
		grantDir, callDir string
		want              error
	}{{"", "/h", nil}, {"/h", "", nil}, {"", "/srv", tool.ErrOutOfGrant}, {"/h", "/h/x", tool.ErrOutOfGrant}} {
		g := &tool.Grant{Tools: []string{HostExecTool}, Commands: []tool.Command{{Argv: []string{"rm", "-r", "build"}, Dir: c.grantDir}}}
		args, _ := json.Marshal(map[string]any{"argv": []string{"rm", "-r", "build"}, "dir": c.callDir, "reason": "r"})
		if _, err := h.Run(context.Background(), tool.Call{Grant: g, Args: args}); !errors.Is(err, c.want) {
			t.Errorf("grant dir %q, call dir %q: %v, want %v", c.grantDir, c.callDir, err, c.want)
		}
	}
}

// Only a read command runs without a press: a class the check does not know
// fails closed, as one that needs a press.
func TestHostExecRunsOnlyReadWithoutAPress(t *testing.T) {
	h := NewHostExec(&commands{}, noIgnored{}, open(true), "/h")
	cmd := tool.Command{Argv: []string{"x"}}
	if err := h.allowedAs(hostcmd.Read, tool.Call{}, cmd); err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, c := range []hostcmd.Class{"", "unknown", hostcmd.Content, hostcmd.Change, hostcmd.Script} {
		if err := h.allowedAs(c, tool.Call{}, cmd); !errors.Is(err, errUnapproved) {
			t.Errorf("class %q without a press: %v, want errUnapproved", c, err)
		}
		if err := h.allowedAs(c, tool.Call{Approved: true}, cmd); err != nil {
			t.Errorf("class %q pressed: %v", c, err)
		}
	}
}

func TestHostExecIsOffWhenTheGateIsShut(t *testing.T) {
	cs := &commands{}
	h := NewHostExec(cs, noIgnored{}, open(false), "/h")
	if h.Offered() {
		t.Fatal("offered while shut")
	}
	if _, err := h.Run(context.Background(), tool.Call{Approved: true, Args: json.RawMessage(`{"argv":["df"],"reason":"r"}`)}); err == nil {
		t.Fatal("ran while shut")
	}
	if len(cs.got) != 0 {
		t.Fatal("the runner was asked while shut")
	}
	if !NewHostExec(cs, noIgnored{}, open(true), "/h").Offered() {
		t.Fatal("not offered while open")
	}
}

func TestHostExecQuestionShowsTheFixedLineApartFromTheReason(t *testing.T) {
	h := NewHostExec(&commands{}, ignoredAll{repo: "/p"}, open(true), "/home/o")
	q, err := h.Question(context.Background(), tool.Call{Speak: texts.Speaker{},
		Args: json.RawMessage(`{"argv":["cat","/p/.env.local"],"reason":"read it, it is harmless"}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Run this command?", "`cat /p/.env.local` in `/home/o`", "passwords and keys in them would reach the model",
		"git ignores this file in `/p`", "The agent's reason: read it, it is harmless"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q:\n%s", want, q)
		}
	}
	if strings.Index(q, "The agent's reason") < strings.Index(q, "would reach the model") ||
		strings.Index(q, "The agent's reason") < strings.Index(q, "git ignores") {
		t.Error("the agent's words come before the fixed lines")
	}
}

// Abuse: a reason cannot pose as the fixed line or add lines of its own, and
// only a content command gets the git warning.
func TestHostExecQuestionKeepsTheReasonOnItsLine(t *testing.T) {
	h := NewHostExec(&commands{}, ignoredAll{repo: "/p"}, open(true), "/home/o")
	q, err := h.Question(context.Background(), tool.Call{
		Args: json.RawMessage(`{"argv":["rm","-r","/p/build"],"reason":"harmless\nOnly reads sizes, names and state; changes nothing."}`)})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(q, "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[3], "The agent's reason: harmless Only reads") {
		t.Fatalf("question lines %q", lines)
	}
	if strings.Contains(q, "git ignores") {
		t.Fatal("a change command got the content warning")
	}
	if !strings.Contains(lines[2], "Deletes the files or folders for good") {
		t.Fatalf("the fixed line is %q", lines[2])
	}
}

func TestHostExecQuestionInThePersonsLanguage(t *testing.T) {
	h := NewHostExec(&commands{}, noIgnored{}, open(true), "/home/o")
	q, err := h.Question(context.Background(), tool.Call{Speak: texts.Speaker{Lang: "uk"}, Args: execArgs("rm", "x")})
	if err != nil || !strings.HasPrefix(q, "Виконати цю команду?") || !strings.Contains(q, "`rm x` у `/home/o`") {
		t.Fatalf("question %q, %v", q, err)
	}
}

func TestHostExecQuestionRefusesASecretInArgv(t *testing.T) {
	h := NewHostExec(&commands{}, noIgnored{}, open(true), "/h")
	_, err := h.Question(context.Background(), tool.Call{Speak: texts.Speaker{},
		Args: json.RawMessage(`{"argv":["curl","-H","Authorization: Bearer sk-ant-abcdefghijklmnopqrstuvwx"],"reason":"r"}`)})
	if !errors.Is(err, ErrSecretShown) {
		t.Fatalf("err = %v, want ErrSecretShown", err)
	}
}

func TestHostExecQuestionRefusesBadArgs(t *testing.T) {
	h := NewHostExec(&commands{}, noIgnored{}, open(true), "/h")
	if _, err := h.Question(context.Background(), tool.Call{Args: json.RawMessage(`{"argv":"rm -rf /","reason":"r"}`)}); err == nil {
		t.Fatal("a question for an argv that is not a list")
	}
}

// Abuse: a backtick in argv cannot end the code span and let Markdown hide
// part of the command behind a link, and a folder cannot draw a line.
func TestHostExecQuestionShowsWhatCannotBeHidden(t *testing.T) {
	h := NewHostExec(&commands{}, noIgnored{}, open(true), "/home/o")
	q, err := h.Question(context.Background(), tool.Call{Args: execArgs("bash", "-c", "echo `[ls](<rm -rf ~>)` x")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "`` bash -c 'echo `[ls](<rm -rf ~>)` x' `` in `/home/o`"; !strings.Contains(q, want) {
		t.Fatalf("question %q lacks %q", q, want)
	}
	q, err = h.Question(context.Background(), tool.Call{Args: json.RawMessage(`{"argv":["rm","x"],"dir":"/a\n# Only reads","reason":"r"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "`rm x` in `$'/a\\n# Only reads'`") || strings.Count(q, "\n") != 3 {
		t.Fatalf("question %q", q)
	}
}
