package modtrial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// Budget is the time a whole trial may take.
const Budget = 60 * time.Second

// Kinds of trigger a step pulls (Trigger.Kind).
const (
	kindCell = "cell"
	kindCron = "cron"
	kindTool = "tool"
)

// Kinds only a trial raises or names for a run the node cannot judge the
// module on; they follow the node's kinds in modhost.
const (
	kindTimeout = "module.timeout" // the module ran past its time limit
	kindTrap    = "module.trap"    // a trap, or an answer the node cannot read
	kindBudget  = "trial.budget"   // the trial's own time ran out during the step
	kindStale   = "stale"          // a kind a module may report, as in modhost
)

// Options are what the host adds to a run.
type Options struct {
	Start time.Time // virtual time at each example's start
	Log   *slog.Logger
	// Current, when set, seeds a run on the node's current cells: their ids
	// and values, read by the host. Nil skips that run.
	Current map[string]Seeded
}

// Seeded is a cell of the node as the host read it.
type Seeded struct {
	Value json.RawMessage
	At    time.Time
}

// Run runs wasm with manifest m on ex. A load failure is an error; every
// failure of the module is in the report.
func Run(ctx context.Context, wasm []byte, m runner.Manifest, prefix string, ex Examples, o Options) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	rep := Report{Passed: true}
	for _, e := range ex.Examples {
		r, err := runExample(ctx, wasm, m, prefix, e, o, nil)
		if err != nil {
			return Report{}, err
		}
		rep.Examples = append(rep.Examples, r)
		rep.Passed = rep.Passed && r.Passed
	}
	if len(o.Current) > 0 {
		now := Example{Name: "now", Steps: []Step{{Trigger: Trigger{Kind: kindCell}}}}
		r, err := runExample(ctx, wasm, m, prefix, now, o, o.Current)
		if err != nil {
			return Report{}, err
		}
		rep.Now = &r.Steps[0]
	}
	return rep, nil
}

// exampleRun is one example's sandbox.
type exampleRun struct {
	mod    *runner.Module
	m      runner.Manifest
	clock  *clock
	cells  *trialCells
	notify *trialNotify
}

// runExample runs one example in a fresh module with an empty state. seed,
// when set, puts the node's current cells in first (each at its own time)
// and the cell step then lists all of them as changed.
func runExample(ctx context.Context, wasm []byte, m runner.Manifest, prefix string, e Example, o Options, seed map[string]Seeded) (ExampleResult, error) {
	res := ExampleResult{Name: e.Name, Passed: true}
	for i := range e.Steps {
		res.Steps = append(res.Steps, StepResult{N: i + 1})
	}
	if ctx.Err() != nil { // no time left: nothing ran
		res.Passed = false
		return res, nil
	}
	clk := &clock{now: o.Start}
	x := &exampleRun{
		clock:  clk,
		cells:  &trialCells{clock: clk, values: map[string]trialCell{}},
		notify: &trialNotify{clock: clk, days: map[string]int{}},
	}
	if m.Capabilities.Notify != nil {
		x.notify.perDay = m.Capabilities.Notify.PerDay
	}
	for id, s := range seed {
		x.cells.set(id, s.Value, s.At)
	}
	m.ConfigValues = configValues(m, e.Config)
	x.m = m
	mod, err := x.load(ctx, wasm, e, prefix, o.Log)
	if err != nil {
		if ctx.Err() != nil { // the budget ended inside the load: a cut trial, not a broken module
			res.Passed = false
			return res, nil
		}
		return ExampleResult{}, fmt.Errorf("modtrial: load: %w", err)
	}
	defer func() { _ = mod.Close(context.WithoutCancel(ctx)) }()
	x.mod = mod

	// modhost runs init once when it loads a module; so does the trial. A
	// failed init is the first step's failure and nothing after it runs.
	if err := mod.Init(ctx); err != nil {
		res.Steps[0] = StepResult{N: 1, Ran: true, Kind: initKind(ctx, err)}
		res.Passed = false
		return res, nil
	}

	for i, s := range e.Steps {
		if ctx.Err() != nil {
			res.Passed = false
			break
		}
		changed := listened(m, slices.Sorted(maps.Keys(s.Cells)))
		if seed != nil {
			changed = listened(m, slices.Sorted(maps.Keys(seed)))
		}
		res.Steps[i] = x.step(ctx, i+1, s, changed)
		res.Passed = res.Passed && res.Steps[i].OK
	}
	return res, nil
}

// load loads the module with the example's sandbox ports.
func (x *exampleRun) load(ctx context.Context, wasm []byte, e Example, prefix string, log *slog.Logger) (*runner.Module, error) {
	secrets := e.Secrets
	return runner.Load(ctx, wasm, x.m, runner.Deps{
		Clock:   x.clock.Now,
		Quiet:   true,
		State:   runner.NewMemState(),
		Cells:   x.cells,
		Notify:  x.notify,
		HTTP:    &http.Client{Transport: newCannedTransport(e.HTTP)},
		Secrets: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok },
		Prefix:  prefix,
		Logger:  log,
	})
}

// configValues is the manifest's defaults under the example's settings.
func configValues(m runner.Manifest, over map[string]string) map[string]string {
	vals := map[string]string{}
	for k, c := range m.Config {
		vals[k] = c.Default
	}
	maps.Copy(vals, over)
	return vals
}

// listened keeps the ids a cell trigger of the manifest lists.
func listened(m runner.Manifest, ids []string) []string {
	l := m.Listens()
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !slices.Contains(l, id) })
}

// step advances the clock, sets the cells, fires the trigger and checks
// what must follow. Cells the step sets are stamped with the new time.
func (x *exampleRun) step(ctx context.Context, n int, s Step, changed []string) StepResult {
	x.clock.now = x.clock.now.Add(s.After)
	for id, v := range s.Cells {
		x.cells.set(id, v, x.clock.now)
	}
	x.cells.reported, x.notify.sent = nil, nil
	sr := StepResult{N: n, Ran: true}
	var failed bool
	if in, ok := input(s.Trigger, changed); ok {
		out, err := x.mod.Handle(ctx, in)
		sr.out = out.Result
		sr.Kind = failureKind(ctx, out, err)
		failed = sr.Kind != ""
	}
	sr.Messages = slices.Clone(x.notify.sent)
	sr.Reports = slices.Clone(x.cells.reported)
	sr.Mismatch = mismatch(s.Expect, sr, failed)
	sr.OK = sr.Mismatch == ""
	if failed && !s.Expect.Fails {
		sr.OK = false // an unexpected failure; the kind says why
	}
	if sr.Kind == kindBudget {
		sr.OK = false // a step cut by the trial's own time never passes
	}
	return sr
}

// input is the module input of a trigger; a step with no trigger only sets.
func input(t Trigger, changed []string) (pdk.Input, bool) {
	switch t.Kind {
	case kindCron:
		return pdk.Input{Trigger: kindCron}, true
	case kindTool:
		args := t.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		return pdk.Input{Trigger: "tool:" + t.Tool, Payload: args}, true
	case kindCell:
		if changed == nil {
			changed = []string{}
		}
		p, _ := json.Marshal(struct {
			Cells    []string `json:"cells"`
			Producer string   `json:"producer"`
		}{changed, "trial"})
		return pdk.Input{Trigger: kindCell, Payload: p}, true
	}
	return pdk.Input{}, false
}

// failureKind is the kind a call failed with, "" when it did not. It
// mirrors how modhost words a call (judge, moduleKind): a module's own kind
// counts only when it is one a module may report, anything else is
// module.exception, so no module text becomes a kind.
func failureKind(ctx context.Context, out pdk.Output, err error) string {
	switch {
	case errors.Is(err, runner.ErrTimeout):
		return kindTimeout
	case err != nil && ctx.Err() != nil:
		return kindBudget
	case err != nil:
		return kindTrap
	case out.OK:
		return ""
	}
	switch out.Kind {
	case pdk.KindAuthFailed, pdk.KindFormat, kindStale:
		return out.Kind
	}
	return pdk.KindException
}

// initKind is the kind a failed init has: the runner's ModuleError carries
// the node's kind already (as modhost.initOutcome reads it); any other error
// is worded like a failed call.
func initKind(ctx context.Context, err error) string {
	if me, ok := errors.AsType[*runner.ModuleError](err); ok {
		switch me.Kind {
		case pdk.KindAuthFailed, pdk.KindFormat, kindStale:
			return me.Kind
		}
		return pdk.KindException
	}
	return failureKind(ctx, pdk.Output{}, err)
}

// mismatch compares a step with its expectations, in the node's words.
func mismatch(e Expect, sr StepResult, failed bool) string {
	var parts []string
	if e.Messages != nil && *e.Messages != len(sr.Messages) {
		parts = append(parts, fmt.Sprintf("expected %s, got %d", plural(*e.Messages, "message"), len(sr.Messages)))
	}
	if e.Reports != nil {
		want := sortedSet(e.Reports)
		got := make([]string, 0, len(sr.Reports))
		for _, r := range sr.Reports {
			got = append(got, r.Cell)
		}
		got = sortedSet(got)
		if !slices.Equal(want, got) {
			parts = append(parts, fmt.Sprintf("expected reports of [%s], got [%s]", strings.Join(want, " "), strings.Join(got, " ")))
		}
	}
	if e.Fails && !failed {
		parts = append(parts, "expected the step to fail, it did not")
	}
	return strings.Join(parts, "; ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func sortedSet(ids []string) []string {
	s := slices.Clone(ids)
	slices.Sort(s)
	return slices.Compact(s)
}
