package modhost

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/tool"
)

// trigger is what started a run, for the metrics: a closed set.
type trigger string

// Triggers.
const (
	triggerCron trigger = "cron"
	triggerTool trigger = "tool"
)

// noArgs is the input schema of a module's tool: it takes no arguments.
var noArgs = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

// moduleTool is one tool trigger of a running module, named
// <short name>.<tool> by the node, so it collides neither with a built-in
// tool nor with another module's (ADR-0062).
type moduleTool struct {
	h       *Host
	module  string
	trigger string // "tool:<name>"
	spec    tool.Spec
}

// Spec implements tool.Handler: a read, run without a press, for admins
// (design section 8.1).
func (t moduleTool) Spec() tool.Spec { return t.spec }

// Run implements tool.Handler: the module's answer, checked against the
// manifest's output by the runner, as JSON; outside content when it holds a
// string (R-RUN-13). A failure is the node's words only (R-RUN-14).
func (t moduleTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	return t.h.runTool(ctx, t, c)
}

// toolsOf are l's tools.
func (h *Host) toolsOf(l *loaded) []tool.Handler {
	var out []tool.Handler
	for _, tr := range l.m.Triggers {
		if tr.Tool == "" {
			continue
		}
		about := tr.Description()
		if about == "" {
			about = "A tool of the owner's module " + l.short + "."
		}
		out = append(out, moduleTool{h: h, module: l.name, trigger: "tool:" + tr.Tool, spec: tool.Spec{
			Name: l.short + "." + tr.Tool, Description: about, InputSchema: noArgs,
			Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true,
		}})
	}
	return out
}

// errNotRunning is a call of a module that is not running.
var errNotRunning = errors.New("the module is not running")

// runTool runs one tool call and settles it.
func (h *Host) runTool(ctx context.Context, t moduleTool, c tool.Call) (tool.Result, error) {
	r, ok := h.call(ctx, t.module, pdk.Input{Trigger: t.trigger, Payload: c.Args})
	if !ok {
		return tool.Result{}, errors.New(t.spec.Name + ": " + errNotRunning.Error())
	}
	o := judge(r.out, r.err, t.spec.Name)
	disabled, serr := h.settle(ctx, r.l, triggerTool, o)
	if serr != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", serr)
	}
	if !o.failed() {
		return tool.Result{Text: string(r.out.Result), External: runner.HasString(r.out.Result)}, nil
	}
	return tool.Result{}, errors.New(toolError(t.spec.Name, o, disabled))
}

// toolError is the node's words for a failed tool call: a contract break
// as the node describes it, any other failure by its kind; never the
// module's text (R-RUN-14).
func toolError(name string, o outcome, disabled bool) string {
	var msg string
	switch o.result {
	case ResultFormat:
		msg = o.format
	case ResultTimeout:
		msg = name + ": the module ran past its time limit (" + string(o.kind) + ")"
	case ResultOK, ResultFailed, ResultException:
		msg = name + ": the module failed (" + string(o.kind) + ")"
	}
	if disabled {
		msg += "; the module is now off: it failed 5 times within an hour"
	}
	return msg
}

// RunCron runs module's cron trigger and settles it, with no model
// (R-RUN-12). A module that is not running does nothing; an error is the
// node's failure to disable a crash-looping module.
func (h *Host) RunCron(ctx context.Context, module string) error {
	r, ok := h.call(ctx, module, pdk.Input{Trigger: string(triggerCron)})
	if !ok {
		h.d.Log.Info("modhost: the cron job of a module that is not running", "module", module)
		return nil
	}
	_, err := h.settle(ctx, r.l, triggerCron, judge(r.out, r.err, r.l.short+" cron"))
	return err
}

// ran is one call of a module: the module, its answer, the runner's error.
type ran struct {
	l   *loaded
	out pdk.Output
	err error
}

// call runs one call of module, holding h.mu for reading so it is not
// unloaded meanwhile; ok is false when it is not running.
func (h *Host) call(ctx context.Context, module string, in pdk.Input) (ran, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	l, ok := h.running[module]
	if !ok {
		return ran{}, false
	}
	out, err := l.mod.Handle(ctx, in)
	return ran{l: l, out: out, err: err}, true
}
