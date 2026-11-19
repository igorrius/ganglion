//go:build lab

package chatlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/turns"
)

// DefaultTurnLimit is how long a step waits for the node to go quiet.
const DefaultTurnLimit = 6 * time.Minute

// RunOptions say what a run does.
type RunOptions struct {
	Scenarios []Scenario
	Models    settings.Models
	Keys      map[string]string
	Relay     string
	Judge     *Judge        // nil: judge steps are not run
	MaxTokens int           // 0: no cap
	Repeat    int           // >0 overrides Scenario.Repeat, for the fault runs
	TurnLimit time.Duration // 0: DefaultTurnLimit
	Tokens    *Counter      // counts the node's models and, by its Wrap, the judge's; nil: a new one
}

// RunAll runs every scenario. Nothing in one scenario stops the next.
func RunAll(ctx context.Context, o RunOptions) Run {
	if o.TurnLimit == 0 {
		o.TurnLimit = DefaultTurnLimit
	}
	if o.Tokens == nil {
		o.Tokens = &Counter{}
	}
	r := Run{Started: time.Now(), Secrets: slices.Collect(maps.Values(o.Keys))}
	for _, s := range o.Scenarios {
		repeats := s.Repeat
		if o.Repeat > 0 {
			repeats = o.Repeat
		}
		rs := make([]Repeat, 0, repeats)
		for range repeats {
			if o.MaxTokens > 0 && o.Tokens.Total() >= o.MaxTokens {
				rs = append(rs, Repeat{NotRun: fmt.Sprintf("the cap of %d tokens was reached", o.MaxTokens)})
				continue
			}
			rs = append(rs, runRepeat(ctx, o, s))
		}
		r.Outcomes = append(r.Outcomes, Decide(s.Name, s.Why, rs))
	}
	r.Tokens = o.Tokens.Total()
	return r
}

// repeatRun is one repeat under way.
type repeatRun struct {
	o     RunOptions
	n     *Node
	rep   Repeat
	log   strings.Builder
	shown []Message // what the person saw after the last step
	last  []Message // the model's words since the last action
}

// errStopRepeat ends a repeat early; its reason is already in rep.
var errStopRepeat = errors.New("chatlab: repeat stopped")

func runRepeat(ctx context.Context, o RunOptions, s Scenario) Repeat {
	if why := missingTool(o, s); why != "" {
		return Repeat{NotRun: why}
	}
	before := o.Tokens.Total()
	n, err := Start(ctx, NodeOptions{Models: o.Models, Keys: o.Keys, Relay: o.Relay, Scenario: s, Tokens: o.Tokens})
	if err != nil {
		return Repeat{NotRun: "the node did not start: " + err.Error()}
	}
	run := &repeatRun{o: o, n: n}
	defer func() { _ = n.Stop(context.WithoutCancel(ctx)) }()
	for _, st := range s.Steps {
		if err := run.step(ctx, st); err != nil {
			if !errors.Is(err, errStopRepeat) {
				run.rep.Failures = append(run.rep.Failures, err.Error())
			}
			break
		}
	}
	run.finish(ctx, s)
	run.rep.Tokens = o.Tokens.Total() - before
	run.rep.Transcript = run.log.String()
	return run.rep
}

// missingTool says what a scenario needs that this machine lacks.
func missingTool(o RunOptions, s Scenario) string {
	if s.Model == ModelScripted {
		return ""
	}
	for _, p := range o.Models.Providers {
		if p.Type != settings.TypeClaudeCLI {
			continue
		}
		if _, err := os.Stat(o.Relay); err != nil {
			return fmt.Sprintf("the relay %s is missing: run make build", o.Relay)
		}
	}
	return ""
}

func (r *repeatRun) step(ctx context.Context, st Step) error {
	switch {
	case st.Say != "", st.Press != "", st.Send != nil, st.Reply != nil:
		r.note(st)
		if err := Do(ctx, r.n.Peer(), st); err != nil {
			return err
		}
		return r.settle(ctx)
	case st.Adds != nil:
		fmt.Fprintf(&r.log, "\n**owner:** %s\n\n_%v later:_\n\n**owner:** %s\n", st.Adds.Say, st.Adds.After, st.Adds.Then)
		if err := Do(ctx, r.n.Peer(), Step{Say: st.Adds.Say}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(st.Adds.After):
		}
		if err := Do(ctx, r.n.Peer(), Step{Say: st.Adds.Then}); err != nil {
			return err
		}
		return r.settle(ctx)
	case st.Advance != nil:
		clock, d := clockConfirmations, st.Advance.Confirmations
		if st.Advance.Scheduler > 0 {
			clock, d = clockScheduler, st.Advance.Scheduler
		}
		fmt.Fprintf(&r.log, "\n_The %s clock moves %v forward._\n", clock, d)
		if err := r.n.Advance(clock, d); err != nil {
			return err
		}
		return r.settle(ctx)
	case st.SignIn:
		fmt.Fprintf(&r.log, "\n**owner signs in** at the card's link and pastes the line the page shows\n")
		if err := r.n.SignIn(ctx); err != nil {
			return err
		}
		return r.settle(ctx)
	case st.Reach != nil:
		return r.reach(ctx, *st.Reach)
	case st.Facts != nil:
		return r.facts(ctx, *st.Facts)
	case st.Language != "":
		r.language(st.Language)
		return nil
	}
	return r.judge(ctx, st.Judge)
}

func (r *repeatRun) note(st Step) {
	switch {
	case st.Say != "":
		fmt.Fprintf(&r.log, "\n**owner:** %s\n", st.Say)
	case st.Press != "":
		fmt.Fprintf(&r.log, "\n**owner presses** [%s]\n", st.Press)
	case st.Reply != nil:
		fmt.Fprintf(&r.log, "\n**owner replies** to the message with %q: %s\n", st.Reply.To, st.Reply.Say)
	default:
		fmt.Fprintf(&r.log, "\n**owner sends** a %s, %s: %s\n", st.Send.Kind, st.Send.Name, st.Send.Caption)
	}
}

// settle waits until the node is quiet and takes in what appeared: the
// transcript gets everything, and last only the model's words, without
// fixed questions, list entries or edits of older messages.
func (r *repeatRun) settle(ctx context.Context) error {
	err := r.n.Quiet(ctx, r.o.TurnLimit)
	now := r.n.Peer().Messages()
	r.last = nil
	for i, m := range now {
		labels := ""
		if len(m.Buttons) > 0 {
			labels = " `[" + strings.Join(m.Buttons, " | ") + "]`"
		}
		switch {
		case i >= len(r.shown):
			fmt.Fprintf(&r.log, "**bot:** %s%s\n", m.Text, labels)
			if len(m.Buttons) == 0 && !m.Replaced {
				r.last = append(r.last, m)
			}
		case m.Text != r.shown[i].Text || !slices.Equal(m.Buttons, r.shown[i].Buttons):
			fmt.Fprintf(&r.log, "**bot edits:** %s%s\n", m.Text, labels)
		}
	}
	r.shown = now
	return err
}

func (r *repeatRun) reach(ctx context.Context, want Reach) error {
	found := func() (bool, error) {
		v, err := ReadView(ctx, r.n, nil)
		if err != nil {
			return false, err
		}
		return len(matching(want.Journal, v.Journal)) > 0, nil
	}
	ok, err := found()
	if err == nil && !ok && want.Nudge != "" {
		before := r.last
		if err := r.step(ctx, Step{Say: want.Nudge}); err != nil {
			return err
		}
		// What the model wrote before the nudge is still its answer: a judge
		// must read that too.
		r.last = slices.Concat(before, r.last)
		ok, err = found()
	}
	if err != nil {
		return err
	}
	if !ok {
		r.rep.Inconclusive = "the state under test was not reached: no " + describe(want.Journal)
		return errStopRepeat
	}
	return nil
}

func (r *repeatRun) facts(ctx context.Context, f Facts) error {
	v, err := ReadView(ctx, r.n, r.last)
	if err != nil {
		return err
	}
	r.rep.Failures = append(r.rep.Failures, Check(f, v)...)
	return nil
}

// language counts like a judgment, k of n, and needs no model.
func (r *repeatRun) language(want string) {
	ok, why := CheckLanguage(want, words(r.last))
	r.rep.Judgments = append(r.rep.Judgments, Judgment{Claim: "Written in " + want, OK: ok, Reason: why})
}

func (r *repeatRun) judge(ctx context.Context, claim string) error {
	if r.o.Judge == nil {
		r.rep.NotRun = "no judge in this run"
		return errStopRepeat
	}
	if len(r.last) == 0 {
		r.rep.Judgments = append(r.rep.Judgments, Judgment{Claim: claim, Reason: "the model wrote nothing since the last action"})
		return nil
	}
	v, err := r.o.Judge.Check(ctx, claim, words(r.last))
	if err != nil {
		r.rep.NotRun = err.Error()
		return errStopRepeat
	}
	r.rep.Judgments = append(r.rep.Judgments, Judgment{Claim: claim, OK: v.OK, Reason: v.Reason})
	return nil
}

// otherModel is a model other than main that answered a step of the turn, "" when
// none did. A turn's steps each end in one attempt that answered; one past those
// is compaction, on the cheap model, and answered nobody.
func otherModel(attempts []turns.Attempt, steps int, main string) string {
	answered := 0
	for _, a := range attempts {
		if a.Error == turns.EndedForAddition {
			answered++
			continue
		}
		if a.Error != "" {
			continue
		}
		if answered++; answered > steps {
			return ""
		}
		if a.Model != main {
			return a.Model
		}
	}
	return ""
}

// finish reads what the repeat did for the transcript, and sets aside a
// repeat another model answered: the result would not be about the main
// model.
func (r *repeatRun) finish(ctx context.Context, s Scenario) {
	main := ModelScripted
	if s.Model != ModelScripted {
		main = MainModel(r.o.Models)
	}
	if v, err := ReadView(ctx, r.n, nil); err == nil {
		r.log.WriteString("\nJournal:\n" + journalLines(v))
	}
	r.log.WriteString("\nTurns:\n")
	rows, err := turnLines(ctx, r.n, &r.log)
	if err != nil {
		fmt.Fprintf(&r.log, "- %v\n", err)
	}
	for _, t := range rows {
		r.rep.CostUSD += t.cost
		if other := otherModel(t.attempts, t.steps, main); other != "" && r.rep.Inconclusive == "" {
			r.rep.Inconclusive = fmt.Sprintf("%s answered in place of %s", other, main)
		}
	}
	// A failure under another model says nothing about the main one: it stays
	// in the transcript, and the repeat does not count.
	if r.rep.Inconclusive != "" {
		for _, f := range r.rep.Failures {
			fmt.Fprintf(&r.log, "\nSet aside, another model answered: %s\n", f)
		}
		r.rep.Failures = nil
	}
}

// journalLines prints the journal of a view.
func journalLines(v View) string {
	var b strings.Builder
	for _, e := range v.Journal {
		fmt.Fprintf(&b, "- `%s` %s %s tainted=%t args=%s result=%q err=%q\n", e.Key, e.Tool, e.Decision, e.Tainted, e.Args, e.ResultHead, e.Err)
	}
	return b.String()
}

// turnRow is what a repeat keeps of a turn.
type turnRow struct {
	cost     float64
	steps    int
	attempts []turns.Attempt
}

// turnLines prints the node's model turns to w and returns them.
func turnLines(ctx context.Context, n *Node, w *strings.Builder) ([]turnRow, error) {
	rows, err := n.DB().QueryContext(ctx, `SELECT conv, outcome, steps, route, tools, cost_usd FROM turns WHERE outcome != 'command' ORDER BY started_ms`)
	if err != nil {
		return nil, fmt.Errorf("chatlab: turns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []turnRow
	for rows.Next() {
		var conv, outcome, route, tools string
		var t turnRow
		if err := rows.Scan(&conv, &outcome, &t.steps, &route, &tools, &t.cost); err != nil {
			return out, fmt.Errorf("chatlab: turns: %w", err)
		}
		fmt.Fprintf(w, "- %s: %s, %d steps, route %s, tools %s\n", conv, outcome, t.steps, route, tools)
		_ = json.Unmarshal([]byte(route), &t.attempts) // a route that does not parse names no model
		out = append(out, t)
	}
	return out, rows.Err()
}
