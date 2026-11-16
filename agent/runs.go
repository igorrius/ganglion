package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// This file is how the owner sees a module's runs (R-RUN-20, ADR-0091):
// /why on a module's message shows the chain of runs that led to it,
// /modules runs <name> and the card's Runs button list a module's last runs.
// Everything shown is the node's own words and closed sets; a module's text
// never reaches these lines, and a name or cell id that is not a plain one
// shows as "?".

const (
	// ActionModuleRuns is the quick action of a module card's Runs button.
	ActionModuleRuns = "modules.runs"
	// runsButtonTTL is how long a Runs button works.
	runsButtonTTL = 24 * time.Hour
	// unshown stands for a name the node does not show.
	unshown = "?"
	// hopIndent starts a hop's second line.
	hopIndent = "   "
)

// callOrder is the host call kinds in the order a run's calls are listed:
// the runner's constants, a closed set.
var callOrder = []string{runner.CallHTTPFetch, runner.CallCellGet, runner.CallCellReport, runner.CallNotify,
	runner.CallStateGet, runner.CallStatePut, runner.CallSecretGet, runner.CallConfigGet}

// moduleRef is the argument of a Runs button: the module's short name.
type moduleRef struct {
	Module string `json:"module"`
}

// plainShown is s escaped when it passes the node's name pattern, else "?".
func plainShown(s string) string {
	if !modstore.PlainName(s) {
		return unshown
	}
	return texts.Escape(s)
}

// plainList is names shown through plainShown, comma-separated.
func plainList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = plainShown(n)
	}
	return strings.Join(out, ", ")
}

// runClock is at in loc: the time of day today, the date and time on
// another day.
func runClock(at time.Time, loc *time.Location, now time.Time) string {
	if clock.DayStart(at, loc).Equal(clock.DayStart(now, loc)) {
		return at.In(loc).Format("15:04")
	}
	return at.In(loc).Format("02.01 15:04")
}

// numberOnly is s when it is a plain number (a version), else "?".
func numberOnly(s string) string {
	if n, err := strconv.Atoi(s); err != nil || n < 0 || strconv.Itoa(n) != s {
		return unshown
	}
	return s
}

// runCause says what started a run, by its trigger. producer, when not
// empty, is the module whose run wrote the cells (shown in a list of runs).
func (a *Agent) runCause(sp texts.Speaker, h modstore.Hop, producer string) string {
	switch h.Trigger {
	case "cron":
		return sp.Text(texts.RunCauseCron, nil)
	case "tool":
		return sp.Text(texts.RunCauseTool, texts.Vars{"tool": plainShown(h.Tool)})
	case "cell":
		vars := texts.Vars{varCells: plainList(h.Cells)}
		if producer == "" {
			return sp.Text(texts.RunCauseCell, vars)
		}
		vars["producer"] = plainShown(moduleName(producer))
		return sp.Text(texts.RunCauseCellBy, vars)
	}
	return unshown
}

// runResult is "ok", or "failed: <kind>" in the node's words.
func runResult(sp texts.Speaker, h modstore.Hop) string {
	if h.Result == "ok" {
		return sp.Text(texts.RunResultOK, nil)
	}
	return sp.Text(texts.RunResultFailed, texts.Vars{varKind: plainShown(cmp.Or(h.Kind, h.Result))})
}

// runCalls is the second line of a run: its host calls by kind in a fixed
// order, and the cells it wrote; "" when it made none and wrote none.
func runCalls(sp texts.Speaker, h modstore.Hop) string {
	var calls []string
	for _, kind := range callOrder {
		if n := h.Calls[kind]; n > 0 {
			calls = append(calls, kind+" "+strconv.Itoa(n))
		}
	}
	var parts []string
	if len(calls) > 0 {
		parts = append(parts, strings.Join(calls, ", "))
	}
	if len(h.Wrote) > 0 {
		parts = append(parts, sp.Text(texts.RunWrote, texts.Vars{varCells: plainList(h.Wrote)}))
	}
	return strings.Join(parts, " · ")
}

// hopText is one run on one or two lines: `16:30 demo-reader v3, by cron ·
// 0.4 s · ok`, and under it its calls. named shows the module's name (a
// chain crosses modules); producer is passed to runCause.
func (a *Agent) hopText(sp texts.Speaker, h modstore.Hop, named bool, producer string, now time.Time) string {
	head := runClock(h.At, a.cfg.Location, now) + " "
	if named {
		head += plainShown(moduleName(h.Module)) + " "
	}
	head += "v" + numberOnly(h.Version) + ", " + a.runCause(sp, h, producer) + " · " + seconds(h.Took.Milliseconds()) + " · " + runResult(sp, h)
	if calls := runCalls(sp, h); calls != "" {
		head += "\n" + hopIndent + calls
	}
	return head
}

// chainText is the chain that led to run, oldest first, numbered, ending
// with the marker of the message itself.
func (a *Agent) chainText(sp texts.Speaker, run modstore.Run, now time.Time) string {
	hops := append(slices.Clone(run.Chain), run.Hop)
	lines := []string{sp.Text(texts.WhyHeader, nil)}
	for i, h := range hops {
		lines = append(lines, strconv.Itoa(i+1)+". "+a.hopText(sp, h, true, "", now))
	}
	lines = append(lines, sp.Text(texts.WhyThisMessage, nil))
	return strings.Join(lines, "\n")
}

// runsText is a module's last runs, newest first, as the journal holds
// them; none says so.
func (a *Agent) runsText(sp texts.Speaker, name string, runs []modstore.Run, now time.Time) string {
	vars := texts.Vars{varName: plainShown(name)}
	if len(runs) == 0 {
		return sp.Text(texts.RunsNone, vars)
	}
	lines := []string{sp.Text(texts.RunsHeader, vars)}
	for _, r := range runs {
		lines = append(lines, a.hopText(sp, r.Hop, false, r.Producer, now))
	}
	return strings.Join(lines, "\n")
}

// why is /why: replying to a module's message, it shows how the message
// came about, with no model. It is for an admin; the history gets a note
// that names the module and nothing else (R-OBS-10).
func (a *Agent) why(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	got := a.lookupReplied(ctx, t.event.Source, t.event.Conv, t.msg)
	name, ok := "", false
	if got.found {
		name, ok = moduleOf(got.row.Origin)
	}
	if !ok {
		t.reply(a.say(ctx, t, texts.WhyUsage, nil))
		return nil
	}
	text, err := a.whyText(ctx, t, got.row.Origin)
	if err != nil {
		return fmt.Errorf("agent: /why: %w", err)
	}
	t.reply(text)
	t.note(commandPrefix + "the owner ran /why on a message of the module " + name)
	return nil
}

// whyText is the answer for a message of origin o, a module's.
func (a *Agent) whyText(ctx context.Context, t *turn, o *event.Origin) (string, error) {
	if o.Run == "" {
		return a.say(ctx, t, texts.WhyNoRun, nil), nil
	}
	run, found, err := a.d.Local.Run(ctx, o.Module, o.Run)
	if err != nil {
		return "", err
	}
	if !found {
		return a.say(ctx, t, texts.WhyGone, nil), nil
	}
	return a.chainText(a.speaker(ctx, t), run, t.now), nil
}

// runsReply is /modules runs <name>: the module's last runs from its card.
func (a *Agent) runsReply(ctx context.Context, t *turn, name string) (moduleReply, error) {
	c, err := a.d.Local.Card(ctx, name)
	if errors.Is(err, modstore.ErrUnknownModule) {
		return a.unknown(ctx, t, name), nil
	}
	if err != nil {
		return moduleReply{}, err
	}
	return moduleReply{text: a.runsText(a.speaker(ctx, t), c.Name, c.Runs, t.now), note: "the owner listed the runs of " + c.Name}, nil
}

// runsButtons is the Runs button of a module's card, stored for the person
// who asked; none when the node stores no buttons or the module has no runs.
func (a *Agent) runsButtons(ctx context.Context, sp texts.Speaker, t *turn, c modstore.Card) ([]event.Button, error) {
	if a.d.Buttons == (NoButtons{}) || len(c.Runs) == 0 {
		return nil, nil
	}
	args, err := json.Marshal(moduleRef{Module: c.Name})
	if err != nil {
		return nil, err
	}
	b := pending.Button{ID: pending.NewButtonID(), Action: ActionModuleRuns, Label: sp.Label(texts.ButtonRuns),
		Principal: t.msg.From, Conv: t.event.Conv, Args: args, Expires: a.d.Now().Add(runsButtonTTL)}
	if err := a.d.Buttons.AddButtons(ctx, b); err != nil {
		return nil, err
	}
	return []event.Button{{ID: b.ID, Label: b.Label}}, nil
}

// runsButton shows a module's last runs in the card's own message, as
// /modules runs does; a module that is gone says so.
func (a *Agent) runsButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var ref moduleRef
	if err := json.Unmarshal(p.Button.Args, &ref); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("agent: button %s: arguments: %w", p.Button.ID, err)
	}
	c, err := a.d.Local.Card(ctx, ref.Module)
	if errors.Is(err, modstore.ErrUnknownModule) {
		return pending.QuickEdit{Text: p.Speak.Text(texts.ModulesUnknown, texts.Vars{varName: plainShown(ref.Module)})}, nil
	}
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: a.runsText(p.Speak, c.Name, c.Runs, a.d.Now())}, nil
}
