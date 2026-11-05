package builtin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/tool"
)

// The plan's tools. They cost no call of the turn's budget (agent.useTool).
const (
	PlanShowTool = "plan.show"
	PlanStepTool = "plan.step"
)

// Plans is the store of the conversations' plans (plan.Store).
type Plans interface {
	Show(ctx context.Context, c tool.Call, steps []string) error
	Step(ctx context.Context, conv string, n int, st plan.Status, note, outcome string) error
}

// errPlanInTask refuses a plan in a scheduled run, whose conversation is no chat.
var errPlanInTask = errors.New("a scheduled run shows no plan")

// NewPlanTools returns plan.show and plan.step (R-LIVE-8). They display and
// approve nothing, so they read as read tools, for admins; they are not LocalSafe,
// since the message leaves the node. The conversation is the call's: no argument
// can name another.
func NewPlanTools(p Plans) []tool.Handler {
	return []tool.Handler{&planShow{p}, &planStep{p}}
}

func planSpec(name, description, schema string) tool.Spec {
	return tool.Spec{Name: name, Description: description, InputSchema: json.RawMessage(schema),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

type planShow struct{ p Plans }

func (*planShow) Spec() tool.Spec {
	return planSpec(PlanShowTool,
		"Show the person the steps of work with several steps, in plain words, as one message that is then edited as they go. Calling it again changes the same message. It approves nothing.",
		`{"type":"object","properties":{"steps":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":20,"description":"The steps, in order, at most 200 characters each."}},"required":["steps"]}`)
}

func (t *planShow) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Steps []string `json:"steps"`
	}
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, err
	}
	if c.Grant != nil {
		return tool.Result{}, errPlanInTask
	}
	if err := t.p.Show(ctx, c, a.Steps); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: "shown"}, nil
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
