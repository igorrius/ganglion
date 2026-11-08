package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// The plan's tools. They cost no call of the turn's budget (agent.useTool).
const (
	PlanShowTool = "plan.show"
	PlanStepTool = "plan.step"
)

// Plans is the store of the conversations' plans (plan.Store).
type Plans interface {
	Show(ctx context.Context, c tool.Call, steps []plan.Step) error
	Step(ctx context.Context, conv string, n int, st plan.Status, note, outcome string) error
	Approved(ctx context.Context, conv string) ([]tool.Command, error)
	// Check reports whether Show would take the steps: its limits, run
	// before the plan is asked.
	Check(sp texts.Speaker, transport string, steps []plan.Step) error
}

// CommandQuestions words a command's lines in a question (*HostExec). Its
// method is unexported, so no type outside this package can word them: a plan
// asks about a command exactly as host.exec does.
type CommandQuestions interface {
	questionVars(ctx context.Context, sp texts.Speaker, cmd tool.Command, reason string) texts.Vars
}

// Errors of plan.show.
var (
	// errPlanInTask refuses a plan in a scheduled run, whose conversation is no chat.
	errPlanInTask = errors.New("a scheduled run shows no plan")
	// errPlanUnapproved refuses a plan with a command that needs a press
	// and has none: the policy asks first, so reaching here means a bug.
	errPlanUnapproved = errors.New("this command needs the owner's press on the plan")
)

// NewPlanTools returns plan.show and plan.step (R-LIVE-8). They read as read
// tools, for admins; they are not LocalSafe, since the message leaves the node.
// The conversation is the call's: no argument can name another. A plan whose
// steps carry commands that need a press is asked as one question, worded by
// q, and its press approves exactly those commands (R-CMD-7).
func NewPlanTools(p Plans, q CommandQuestions) []tool.Handler {
	return []tool.Handler{&planShow{p: p, q: q}, &planStep{p}}
}

func planSpec(name, description, schema string) tool.Spec {
	return tool.Spec{Name: name, Description: description, InputSchema: json.RawMessage(schema),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

type planShow struct {
	p Plans
	q CommandQuestions
}

func (*planShow) Spec() tool.Spec {
	s := planSpec(PlanShowTool,
		"Show the person the steps of work with several steps, in plain words, as one message that is then edited as they go. "+
			"Calling it again changes the same message. A step may carry the host command it runs (argv, dir, reason). "+
			"If any command needs the person's press, the plan is asked as one question: one press approves exactly those "+
			"commands, in this chat, for an hour; run each with host.exec repeating its argv and dir exactly as approved.",
		`{"type":"object","properties":{"steps":{"type":"array","items":{"anyOf":[{"type":"string","maxLength":200},`+
			`{"type":"object","properties":{"text":{"type":"string"},"argv":{"type":"array","items":{"type":"string"},"maxItems":64},`+
			`"dir":{"type":"string"},"reason":{"type":"string"}},"required":["text"],"additionalProperties":false}]},`+
			`"minItems":1,"maxItems":20,"description":"The steps, in order, at most 200 characters each."}},"required":["steps"]}`)
	s.KeepsQuestion = true
	return s
}

func (t *planShow) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	steps, err := hostcmd.ParsePlan(c.Args)
	if err != nil {
		return tool.Result{}, err
	}
	if c.Grant != nil {
		return tool.Result{}, errPlanInTask
	}
	if err := planAllowed(c, steps); err != nil {
		return tool.Result{}, err
	}
	if err := t.p.Show(ctx, c, planSteps(steps)); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: "shown"}, nil
}

// planSteps are the parsed steps as the store takes them.
func planSteps(steps []hostcmd.PlanStep) []plan.Step {
	out := make([]plan.Step, len(steps))
	for i, s := range steps {
		out[i] = plan.Step{Text: s.Text, Command: s.Command}
	}
	return out
}

// planAllowed checks again what the policy decided, as host.exec does: a
// forbidden command never, a press included; a command that needs a press only
// on the pressed plan, or when the conversation's approved plan lists it. Only
// a read command needs neither: any other class fails closed.
func planAllowed(c tool.Call, steps []hostcmd.PlanStep) error {
	for i, s := range steps {
		if s.Command == nil {
			continue
		}
		switch cl := hostcmd.Classify(s.Command.Argv).Class; {
		case cl == hostcmd.Forbidden:
			return fmt.Errorf("plan.show: step %d: %s never runs: %w", i+1, hostcmd.Show(s.Command.Argv), tool.ErrForbidden)
		case cl == hostcmd.Read || c.Approved:
		case c.Planned != nil && c.Planned(*s.Command):
		default:
			return fmt.Errorf("plan.show: step %d: %w", i+1, errPlanUnapproved)
		}
	}
	return nil
}

// Question implements tool.Questioner: the plan as the person will approve it,
// each command with its folder, and one that needs a press with its fixed line
// and the agent's reason apart (R-CMD-6), in one question that must show whole.
// A plan past the store's limits is refused first, so no press is spent on a
// plan Show would refuse.
func (t *planShow) Question(ctx context.Context, c tool.Call) (string, error) {
	steps, err := hostcmd.ParsePlan(c.Args)
	if err != nil {
		return "", err
	}
	if err := t.p.Check(c.Speak, c.Transport, planSteps(steps)); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(strings.Fields(s.Text), " "))
		if s.Command == nil {
			continue
		}
		v := t.q.questionVars(ctx, c.Speak, *s.Command, s.Reason)
		fmt.Fprintf(&b, "    %s\n", v["command"])
		if hostcmd.Classify(s.Command.Argv).Class.NeedsPress() {
			fmt.Fprintf(&b, "    %s%s\n    %s\n", v["line"], strings.ReplaceAll(v["warning"], "\n", "\n    "), v["reason"])
		}
	}
	return whole(PlanShowTool, c.Speak.Text(texts.AskPlan, texts.Vars{"plan": strings.TrimRight(b.String(), "\n")}))
}

type planStep struct{ p Plans }

func (*planStep) Spec() tool.Spec {
	return planSpec(PlanStepTool,
		"Mark a step of the plan you showed; a step that ended starts the next one. Send it beside your next real call, in the same message, so a tick costs no round trip. Put the result of the work in outcome in the last one.",
		`{"type":"object","properties":{"n":{"type":"integer","minimum":1,"description":"The step's number, from 1."},`+
			`"status":{"type":"string","enum":["running","done","failed","skipped"],"description":"done by default."},`+
			`"note":{"type":"string","description":"One line, such as why it failed."},`+
			`"outcome":{"type":"string","description":"One line with what the work found or changed, true before your answer goes out: you write the answer after this call. Never say that an answer or a report was sent. It stays in the chat."}},"required":["n"]}`)
}

func (t *planStep) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	a := struct {
		N       int         `json:"n"`
		Status  plan.Status `json:"status"`
		Note    string      `json:"note"`
		Outcome string      `json:"outcome"`
	}{Status: plan.Done}
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, err
	}
	if c.Grant != nil {
		return tool.Result{}, errPlanInTask
	}
	if err := t.p.Step(ctx, c.Conv, a.N, a.Status, a.Note, a.Outcome); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: "marked"}, nil
}
