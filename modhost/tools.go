package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
	triggerCell trigger = "cell" // cells it listens to changed (R-RUN-15)
	// triggerEvent: an event reached one of its accepts ports by a link (R-BUS-6).
	triggerEvent trigger = "event"
	// triggerCall: a module was asked through a served port by a link (R-BUS-6).
	triggerCall trigger = "call"
)

// unattended reports whether nobody watches a run of t: a cron, a cell or an
// event trigger's failure is escalated, a tool call's is the calling turn's
// to tell, and a served call's is the caller's.
func (t trigger) unattended() bool { return t != triggerTool && t != triggerCall }

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

// runTool runs one tool call, as the run of the call's key, and settles it.
func (h *Host) runTool(ctx context.Context, t moduleTool, c tool.Call) (tool.Result, error) {
	r, ok := h.call(ctx, t.module, pdk.Input{Trigger: t.trigger, Payload: c.Args}, newRun(c.Key(), triggerTool, 0).byTool(t.spec.Name))
	if !ok {
		return tool.Result{}, errors.New(t.spec.Name + ": " + errNotRunning.Error())
	}
	o := judge(r.out, r.err, t.spec.Name)
	disabled, serr := h.finish(ctx, r, o)
	if serr != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", serr)
	}
	if !o.failed() {
		return tool.Result{Text: string(r.out.Result), External: runner.HasString(r.out.Result)}, nil
	}
	return tool.Result{}, errors.New(toolError(t.spec.Name, r.l.short, o, disabled))
}

// toolError is the node's words for a failed tool call: a contract break
// as the node describes it, any other failure by its kind, each with the
// node's hint for it; never the module's text (R-RUN-14).
func toolError(name, short string, o outcome, disabled bool) string {
	msg := name + ": the module failed (" + string(o.kind) + ")"
	if o.result == ResultFormat {
		msg = o.format
	}
	msg = withHint(msg, o, short)
	if disabled {
		msg += "; the module is now off: it failed 5 times within an hour"
	}
	return msg
}

// RunCron runs module's cron trigger as the run id, the schedule event's
// idempotency key (ruling 3 of M4c), and settles it, with no model
// (R-RUN-12); the cells it wrote start their listeners (R-RUN-15). A module
// that is not running does nothing; an error is the node's failure to
// disable a crash-looping module.
func (h *Host) RunCron(ctx context.Context, module, id string) error {
	r, ok := h.call(ctx, module, pdk.Input{Trigger: string(triggerCron)}, newRun(id, triggerCron, 0))
	if !ok {
		h.d.Log.Info("modhost: the cron job of a module that is not running", "module", module)
		return nil
	}
	_, err := h.finish(ctx, r, judge(r.out, r.err, r.l.short+" cron"))
	return err
}

// ran is one call of a module: the module, the run, its answer, the
// runner's error.
type ran struct {
	l    *loaded
	run  run
	out  pdk.Output
	err  error
	at   time.Time     // when the call started, by the host's clock
	took time.Duration // how long it ran
}

// call runs one call of module as run r, holding h.mu for reading so it is
// not unloaded meanwhile; ok is false when it is not running. The call's
// context carries r, so the host functions' ports see it.
func (h *Host) call(ctx context.Context, module string, in pdk.Input, r run) (ran, bool) {
	return h.callIf(ctx, module, in, r, always)
}

// always is the condition of a call that has none.
func always() bool { return true }

// callIf is call that runs the module only if still holds once the lock is
// held; ok is false when the module is not running or still does not hold.
// A caller that waits for the call's answer may be gone by the time a
// waiting Reload lets the call in (R-BUS-6).
func (h *Host) callIf(ctx context.Context, module string, in pdk.Input, r run, still func() bool) (ran, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	l, ok := h.running[module]
	if !ok || !still() {
		return ran{}, false
	}
	at := h.d.Now()
	out, err := l.mod.Handle(withRun(ctx, r), in)
	return ran{l: l, run: r, out: out, err: err, at: at, took: max(h.d.Now().Sub(at), 0)}, true
}

// finish settles a run judged o and then starts the listeners of the cells
// it wrote, failed or not: the cells changed either way.
func (h *Host) finish(ctx context.Context, r ran, o outcome) (disabled bool, err error) {
	disabled, err = h.settle(ctx, r.l, r.run.Trigger, o)
	h.record(ctx, r, o)
	h.fanOut(ctx, r.l, r.run)
	return disabled, err
}
