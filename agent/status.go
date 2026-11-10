package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/turns"
)

const (
	// statusTimeout bounds each source's answer, so /status stays quick when one hangs.
	statusTimeout = 2 * time.Second
	// limitNear is the share of a daily limit past which the day's line warns.
	limitNear = 0.8
	// turnsSource names the turns log in the line that says it did not answer.
	turnsSource = "turns"
)

// statusMarks are the marks in front of a line and of the verdict.
var statusMarks = map[status.State]string{status.OK: "✅", status.Warn: "⚠️", status.Off: "⏸", status.Fail: "❌"}

// statusTitles are the sections' headings.
var statusTitles = map[status.Section]texts.Key{
	status.SectionNode: texts.StatusSectionNode, status.SectionModels: texts.StatusSectionModels,
	status.SectionDelivery: texts.StatusSectionDelivery, status.SectionProblems: texts.StatusSectionProblems,
}

// showStatus shows an admin how the node is doing, with no model and no note in the history (R-OBS-11).
func (a *Agent) showStatus(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	s := status.Scope{Conv: t.event.Conv, Principal: t.msg.From, Now: t.now, Location: a.cfg.Location}
	checks := append(a.ownStatus(ctx, t, s), status.Collect(ctx, s, a.d.StatusSources, statusTimeout, a.log)...)
	t.reply(renderStatus(status.Order(checks), a.speaker(ctx, t), s))
	return nil
}

// ownStatus is what only the agent knows: the session's models and the day against the limits.
func (a *Agent) ownStatus(ctx context.Context, t *turn, s status.Scope) []status.Check {
	return []status.Check{a.sessionLine(ctx, t), a.dayLine(ctx, t, s)}
}

// sessionLine names the models an ordinary turn of this session tries now, in order, and the model
// that answered its last turn when that was not the first.
func (a *Agent) sessionLine(ctx context.Context, t *turn) status.Check {
	c := status.Check{Section: status.SectionModels, State: status.OK, Line: texts.StatusSession}
	cands, err := a.d.Router.Route(ctx, router.TaskReq{Purpose: router.PurposeChat, Turn: true, Tools: len(a.toolSpecs(t)) > 0, Privacy: t.state.privacy()})
	if err != nil || len(cands) == 0 {
		c.State, c.Line = status.Fail, texts.StatusSessionNone
		return c
	}
	names := make([]string, len(cands))
	for i, cand := range cands {
		names[i] = cand.Model
	}
	c.Vars = texts.Vars{"models": strings.Join(names, ", ")}
	if by := a.lastAnsweredBy(ctx, t.event.Conv); by != "" && by != names[0] {
		c.State, c.Line, c.Vars["model"] = status.Warn, texts.StatusSessionFallback, by
	}
	return c
}

// lastAnsweredBy is the model that answered conv's last turn: its first successful attempt, since a
// compaction after the answer adds its own attempts to the same route. "" for none or when it cannot
// be read.
func (a *Agent) lastAnsweredBy(ctx context.Context, conv string) string {
	last, err := a.d.Turns.Last(ctx, conv)
	if err != nil {
		if !errors.Is(err, turns.ErrNoTurn) {
			a.log.Warn("agent: /status: last turn unreadable", "err", err)
		}
		return ""
	}
	for _, at := range last.Route {
		if at.Error == "" {
			return at.Model
		}
	}
	return ""
}

// dayLine is the asking admin's day against the daily limits, and what the node spent today.
func (a *Agent) dayLine(ctx context.Context, t *turn, s status.Scope) status.Check {
	start := clock.DayStart(s.Now, a.cfg.Location)
	mine, err := a.d.Turns.Totals(ctx, start, t.msg.From, "")
	var all turns.Totals
	if err == nil {
		all, err = a.d.Turns.Totals(ctx, start, "", "")
	}
	if err != nil { // no line rather than a spend of $0.00 that is not known
		a.log.Warn("agent: /status: totals unreadable", "err", err)
		return status.Check{Section: status.SectionModels, State: status.Warn, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": turnsSource}}
	}
	c := status.Check{Section: status.SectionModels, State: status.OK, Line: texts.StatusDay, Vars: texts.Vars{
		"turns": ofLimit(mine.Turns, a.cfg.DailyTurns), "tokens": ofLimit(mine.Tokens, a.cfg.DailyTokens),
		"cost": fmt.Sprintf("$%.2f", all.CostUSD),
	}}
	switch {
	case !under(mine.Turns, a.cfg.DailyTurns) || !under(mine.Tokens, a.cfg.DailyTokens):
		c.State, c.Reason = status.Fail, texts.StatusReasonLimitReached
	case near(mine.Turns, a.cfg.DailyTurns) || near(mine.Tokens, a.cfg.DailyTokens):
		c.State, c.Reason = status.Warn, texts.StatusReasonLimitNear
	}
	return c
}

// ofLimit is "9/10", or "9" when the limit is off.
func ofLimit(used, limit int) string {
	if limit <= 0 {
		return strconv.Itoa(used)
	}
	return strconv.Itoa(used) + "/" + strconv.Itoa(limit)
}

// near reports whether used is past limitNear of an active limit.
func near(used, limit int) bool { return limit > 0 && float64(used) >= limitNear*float64(limit) }

// renderStatus is the reply: the verdict, then each section that has lines under its heading.
func renderStatus(checks []status.Check, sp texts.Speaker, s status.Scope) string {
	var b strings.Builder
	b.WriteString(verdictLine(checks, sp))
	r := lineRenderer{sp: sp, scope: s, units: status.Units{Day: sp.Text(texts.StatusUnitDay, nil), Hour: sp.Text(texts.StatusUnitHour, nil),
		Min: sp.Text(texts.StatusUnitMin, nil), Sec: sp.Text(texts.StatusUnitSec, nil)}}
	for _, sec := range status.Sections {
		r.section(&b, sec, checks)
	}
	return b.String()
}

// lineRenderer writes lines in one reader's language: its texts and its units of time.
type lineRenderer struct {
	sp    texts.Speaker
	scope status.Scope
	units status.Units
}

// verdictLine is the first line: all well, or how many lines need a look or do not work.
func verdictLine(checks []status.Check, sp texts.Speaker) string {
	v, n := status.Verdict(checks)
	key := map[status.State]texts.Key{status.OK: texts.StatusVerdictOK, status.Warn: texts.StatusVerdictWarn, status.Fail: texts.StatusVerdictFail}[v]
	return statusMarks[v] + " " + sp.Text(key, texts.Vars{"n": strconv.Itoa(n)})
}

// section writes sec's heading and lines, or nothing when it has none.
func (r lineRenderer) section(b *strings.Builder, sec status.Section, checks []status.Check) {
	head := false
	for _, c := range checks {
		if c.Section != sec {
			continue
		}
		if !head {
			b.WriteString("\n\n## " + r.sp.Text(statusTitles[sec], nil) + "\n")
			head = true
		}
		b.WriteString("\n- " + r.line(c))
	}
}

// line is one line: its mark, its text, and for a line that is not OK its reason and since when.
func (r lineRenderer) line(c status.Check) string {
	v := r.values(c)
	line := statusMarks[c.State] + " " + r.sp.Text(c.Line, v)
	if c.Reason != "" {
		line += " — " + r.sp.Text(c.Reason, v)
	}
	if c.State != status.OK && !c.Since.IsZero() {
		line += " (" + r.sp.Text(texts.StatusSince, texts.Vars{"time": status.Clock(c.Since, r.scope)}) + ")"
	}
	return line
}

// values are c's values shown as written, its lengths of time in the reader's units.
func (r lineRenderer) values(c status.Check) texts.Vars {
	out := make(texts.Vars, len(c.Vars)+len(c.Spans))
	for k, s := range c.Vars {
		out[k] = texts.Escape(s)
	}
	for k, d := range c.Spans {
		out[k] = texts.Escape(status.Duration(d, r.units))
	}
	return out
}
