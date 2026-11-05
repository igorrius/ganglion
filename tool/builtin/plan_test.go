package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/igorrius/ganglion/plan"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

type fakePlans struct {
	shown  []string
	conv   string
	n      int
	st     plan.Status
	note   string
	end    string
	result error
}

func (f *fakePlans) Show(_ context.Context, c tool.Call, steps []string) error {
	f.shown, f.conv = steps, c.Conv
	return f.result
}

func (f *fakePlans) Step(_ context.Context, conv string, n int, st plan.Status, note, outcome string) error {
	f.conv, f.n, f.st, f.note, f.end = conv, n, st, note, outcome
	return f.result
}

func planTools(f *fakePlans) (show, step tool.Handler) {
	hs := builtin.NewPlanTools(f)
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
	if len(f.shown) != 2 || f.conv != "telegram:42" {
		t.Fatalf("%+v", f)
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
