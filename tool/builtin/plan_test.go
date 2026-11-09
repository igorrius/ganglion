package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

type fakePlans struct {
	shown  []plan.Step
	call   tool.Call
	conv   string
	n      int
	st     plan.Status
	note   string
	end    string
	result error
}

func (f *fakePlans) Show(_ context.Context, c tool.Call, steps []plan.Step) error {
	f.shown, f.call, f.conv = steps, c, c.Conv
	return f.result
}

func (f *fakePlans) Approved(context.Context, string) ([]tool.Command, error) { return nil, f.result }

// Check runs the store's own limits, as the node's store does.
func (f *fakePlans) Check(sp texts.Speaker, transport string, steps []plan.Step) error {
	return plan.Check(sp, transport, "/home/o", steps)
}

// noRun is a host runner a plan never reaches.
type noRun struct{}

func (noRun) Exec(context.Context, host.Request) (host.Answer, error) {
	return host.Answer{}, errors.New("a plan ran a command")
}

// ignored reports every file ignored in a repository, when it is set.
type ignored string

func (g ignored) Ignored(context.Context, string, []string) (string, bool) { return string(g), g != "" }

type gate struct{}

func (gate) Open() bool { return true }

func hostExec(ig ignored) *builtin.HostExec {
	return builtin.NewHostExec(noRun{}, ig, gate{}, "/home/o")
}

func (f *fakePlans) Step(_ context.Context, conv string, n int, st plan.Status, note, outcome string) error {
	f.conv, f.n, f.st, f.note, f.end = conv, n, st, note, outcome
	return f.result
}

func planTools(f *fakePlans) (show, step tool.Handler) {
	hs := builtin.NewPlanTools(f, hostExec(""))
	return hs[0], hs[1]
}

func TestPlanToolsAreForAdminsAndLeaveNoSessionOffline(t *testing.T) {
	show, step := planTools(&fakePlans{})
	for _, h := range []tool.Handler{show, step} {
		sp := h.Spec()
		if sp.Class != tool.ClassRead || sp.Policy != tool.PolicyAuto || !sp.AdminOnly || sp.LocalSafe {
			t.Fatalf("%s: %+v", sp.Name, sp)
		}
	}
	if show.Spec().Name != builtin.PlanShowTool || step.Spec().Name != builtin.PlanStepTool {
		t.Fatalf("names %s, %s", show.Spec().Name, step.Spec().Name)
	}
}

func TestPlanShowPassesTheSteps(t *testing.T) {
	f := &fakePlans{}
	show, _ := planTools(f)
	c := tool.Call{Conv: "telegram:42", Args: json.RawMessage(`{"steps":["a","b"]}`)}
	if err := tool.CheckArgs(show.Spec(), c.Args); err != nil {
		t.Fatal(err)
	}
	if _, err := show.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 2 || f.conv != "telegram:42" || f.shown[0].Text != "a" || f.shown[0].Command != nil {
		t.Fatalf("%+v", f)
	}
}

func planArgs(steps ...any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"steps": steps})
	return b
}

func cmdStep(text, dir string, argv ...string) map[string]any {
	m := map[string]any{"text": text, "argv": argv, "reason": "to free space"}
	if dir != "" {
		m["dir"] = dir
	}
	return m
}

// R-CMD-7: a step may carry its command; the policy asks for the plan when
// one needs a press, so the tool's spec keeps the question as the plan's
// message (ruling 8).
func TestPlanShowTakesStepsWithCommands(t *testing.T) {
	f := &fakePlans{}
	show, _ := planTools(f)
	if !show.Spec().KeepsQuestion {
		t.Fatal("plan.show does not keep its question")
	}
	c := tool.Call{Conv: "telegram:42", Approved: true,
		Args: planArgs("Measure", cmdStep("Clean Go's cache", "/srv", "go", "clean", "-cache"), cmdStep("Look", "", "df", "-h"))}
	if err := tool.CheckArgs(show.Spec(), c.Args); err != nil {
		t.Fatal(err)
	}
	if _, err := show.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 3 || f.shown[0].Command != nil || f.shown[1].Text != "Clean Go's cache" ||
		!f.shown[1].Command.Equal(tool.Command{Argv: []string{"go", "clean", "-cache"}, Dir: "/srv"}) ||
		!f.shown[2].Command.Equal(tool.Command{Argv: []string{"df", "-h"}}) || !f.call.Approved {
		t.Fatalf("shown %+v", f.shown)
	}
}

// The tool checks again what the policy decided: a command that needs a press
// is shown only through the pressed plan, or when an approved plan lists it;
// a forbidden one never, a press included.
func TestPlanShowRefusesWhatThePolicyShouldHaveStopped(t *testing.T) {
	clean := tool.Command{Argv: []string{"go", "clean", "-cache"}}
	planned := func(cmd tool.Command) bool { return cmd.Equal(clean) }
	for name, tc := range map[string]struct {
		c    tool.Call
		want error
	}{
		"read runs":                {tool.Call{Args: planArgs(cmdStep("look", "", "df", "-h"))}, nil},
		"change unapproved":        {tool.Call{Args: planArgs("a", cmdStep("clean", "", "go", "clean", "-cache"))}, errAny},
		"change pressed":           {tool.Call{Approved: true, Args: planArgs(cmdStep("clean", "", "go", "clean", "-cache"))}, nil},
		"change planned":           {tool.Call{Planned: planned, Args: planArgs(cmdStep("clean", "", "go", "clean", "-cache"))}, nil},
		"planned elsewhere":        {tool.Call{Planned: planned, Args: planArgs(cmdStep("clean", "/x", "go", "clean", "-cache"))}, errAny},
		"forbidden":                {tool.Call{Args: planArgs(cmdStep("run", "", "docker", "run", "x"))}, tool.ErrForbidden},
		"forbidden though pressed": {tool.Call{Approved: true, Args: planArgs("a", cmdStep("run", "", "sudo", "true"))}, tool.ErrForbidden},
		"forbidden though planned": {tool.Call{Planned: func(tool.Command) bool { return true },
			Args: planArgs(cmdStep("run", "", "sudo", "true"))}, tool.ErrForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePlans{}
			show, _ := planTools(f)
			_, err := show.Run(context.Background(), tc.c)
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("err %v", err)
			case tc.want == nil:
				return
			case err == nil || f.shown != nil:
				t.Fatalf("shown %+v, err %v", f.shown, err)
			case tc.want != errAny && !errors.Is(err, tc.want):
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
}

// errAny stands for any error in a table.
var errAny = errors.New("any error")

// A refused step is named, so the model can fix that one.
func TestPlanShowNamesTheRefusedStep(t *testing.T) {
	show, _ := planTools(&fakePlans{})
	_, err := show.Run(context.Background(), tool.Call{Approved: true, Args: planArgs("a", "b", cmdStep("run", "", "sudo", "true"))})
	if err == nil || !strings.Contains(err.Error(), "step 3") || !strings.Contains(err.Error(), "sudo true") {
		t.Fatalf("err %v", err)
	}
}

// R-CMD-6, R-CMD-7: the plan is the question: numbered steps; each command
// with its folder, and a command that needs a press with its fixed line, the
// git warning and the agent's reason apart, after the fixed lines.
func TestPlanQuestionShowsEachCommandWithItsLines(t *testing.T) {
	show := builtin.NewPlanTools(&fakePlans{}, hostExec("/p"))[0].(tool.Questioner)
	q, err := show.Question(context.Background(), tool.Call{
		Args: planArgs("Measure", cmdStep("Read the settings", "", "cat", "/p/.env"), cmdStep("Clean", "/srv", "go", "clean", "-cache"),
			cmdStep("Look again", "", "df", "-h"))})
	if err != nil {
		t.Fatal(err)
	}
	want := "Run this plan? One press approves exactly the commands it lists, in this chat, for an hour, and nothing else.\n\n" +
		"1. Measure\n" +
		"2. Read the settings\n" +
		"    `cat /p/.env` in `/home/o`\n" +
		"    " + texts.Speaker{}.Text(texts.CmdContent, nil) + "\n" +
		"    " + texts.Speaker{}.Text(texts.CmdGitIgnored, texts.Vars{"repo": "`/p`"}) + "\n" +
		"    The agent's reason: to free space\n" +
		"3. Clean\n" +
		"    `go clean -cache` in `/srv`\n" +
		"    " + texts.Speaker{}.Text(texts.CmdGoClean, nil) + "\n" +
		"    The agent's reason: to free space\n" +
		"4. Look again\n" +
		"    `df -h` in `/home/o`"
	if q != want {
		t.Fatalf("got\n%s\nwant\n%s", q, want)
	}
}

func TestPlanQuestionInThePersonsLanguage(t *testing.T) {
	show := builtin.NewPlanTools(&fakePlans{}, hostExec(""))[0].(tool.Questioner)
	q, err := show.Question(context.Background(), tool.Call{Speak: texts.Speaker{Lang: "uk"}, Args: planArgs(cmdStep("x", "", "rm", "x"))})
	if err != nil || !strings.HasPrefix(q, "Виконати цей план?") || !strings.Contains(q, "`rm x` у `/home/o`") {
		t.Fatalf("question %q, %v", q, err)
	}
}

// Abuse: a step's words and a reason cannot add lines that pose as a
// command's fixed line, and a secret in argv refuses the question, so the
// owner sees all they approve.
func TestPlanQuestionCannotBeForged(t *testing.T) {
	show := builtin.NewPlanTools(&fakePlans{}, hostExec(""))[0].(tool.Questioner)
	step := map[string]any{"text": "Clean\n    `df -h` in `/home/o`", "argv": []string{"rm", "-r", "/srv"},
		"reason": "fine\n    Only reads sizes, names and state; changes nothing."}
	q, err := show.Question(context.Background(), tool.Call{Args: planArgs(step)})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(q, "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[2], "1. Clean ") || !strings.HasPrefix(lines[5], "    The agent's reason: fine Only reads") {
		t.Fatalf("question lines %q", lines)
	}
	_, err = show.Question(context.Background(), tool.Call{
		Args: planArgs(cmdStep("x", "", "curl", "-H", "Authorization: Bearer sk-ant-abcdefghijklmnopqrstuvwx"))})
	if !errors.Is(err, builtin.ErrSecretShown) {
		t.Fatalf("err = %v, want ErrSecretShown", err)
	}
	if _, err := show.Question(context.Background(), tool.Call{Args: json.RawMessage(`{"steps":[{"text":"x","argv":"rm -rf /"}]}`)}); err == nil {
		t.Fatal("a question for an argv that is not a list")
	}
}

// A plan goes to the conversation of the call: no argument names another.
func TestPlanHasNoArgumentForAConversation(t *testing.T) {
	show, step := planTools(&fakePlans{})
	for _, h := range []tool.Handler{show, step} {
		if err := tool.CheckArgs(h.Spec(), json.RawMessage(`{"conv":"telegram:7"}`)); err == nil {
			t.Fatalf("%s took a conversation", h.Spec().Name)
		}
	}
}

func TestPlanStepDefaultsToDone(t *testing.T) {
	f := &fakePlans{}
	_, step := planTools(f)
	c := tool.Call{Conv: "telegram:42", Args: json.RawMessage(`{"n":2,"note":"x","outcome":"y"}`)}
	if _, err := step.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if f.n != 2 || f.st != plan.Done || f.note != "x" || f.end != "y" {
		t.Fatalf("%+v", f)
	}
}

func TestPlanToolsRefuseBadInput(t *testing.T) {
	f := &fakePlans{}
	show, step := planTools(f)
	ctx := context.Background()
	for name, run := range map[string]func() error{
		"steps of the wrong type": func() error {
			_, err := show.Run(ctx, tool.Call{Args: json.RawMessage(`{"steps":"a"}`)})
			return err
		},
		"a scheduled run": func() error {
			_, err := show.Run(ctx, tool.Call{Grant: &tool.Grant{}, Args: json.RawMessage(`{"steps":["a"]}`)})
			return err
		},
		"a step in a scheduled run": func() error {
			_, err := step.Run(ctx, tool.Call{Grant: &tool.Grant{}, Args: json.RawMessage(`{"n":1}`)})
			return err
		},
	} {
		if run() == nil {
			t.Errorf("%s passed", name)
		}
	}
	f.result = errors.New("nope")
	if _, err := step.Run(ctx, tool.Call{Args: json.RawMessage(`{"n":1}`)}); err == nil {
		t.Error("the store's error did not reach the model")
	}
}

// The owner's live try: the last tick said the report was sent, and it came ten
// seconds later. The model writes its answer after this call.
func TestPlanOutcomeIsNotAboutDelivery(t *testing.T) {
	_, step := planTools(&fakePlans{})
	if s := string(step.Spec().InputSchema); !strings.Contains(s, "Never say that an answer or a report was sent") {
		t.Fatalf("schema %s", s)
	}
}

// A plan past the store's limits is refused before it is asked, so the owner
// never spends a press on a plan Show would refuse.
func TestPlanQuestionRunsThePlansLimits(t *testing.T) {
	show := builtin.NewPlanTools(&fakePlans{}, hostExec(""))[0].(tool.Questioner)
	many := make([]any, 21)
	for i := range many {
		many[i] = "step"
	}
	long := strings.Repeat("x", 1000)
	for name, args := range map[string]json.RawMessage{
		"21 steps":   planArgs(many...),
		"empty text": planArgs("  "),
		"too long": planArgs(cmdStep("a", "", "echo", long), cmdStep("b", "", "echo", long),
			cmdStep("c", "", "echo", long), cmdStep("d", "", "echo", long)),
	} {
		if _, err := show.Question(context.Background(), tool.Call{Args: args}); !errors.Is(err, plan.ErrLimit) {
			t.Errorf("%s: %v, want plan.ErrLimit", name, err)
		}
	}
}
