package agent

import (
	"context"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
)

// overLimit checks the principal's daily turns and tokens. Over a limit it
// returns one alert per admin; their keys repeat for the day, so the outbox
// sends each alert once.
func (a *Agent) overLimit(ctx context.Context, now time.Time, principal, eventID string) ([]event.Outgoing, bool) {
	if a.cfg.DailyTurns <= 0 && a.cfg.DailyTokens <= 0 {
		return nil, false
	}
	day := clock.DayStart(now, a.cfg.Location)
	tot, err := a.d.Turns.Totals(ctx, day, principal, eventID)
	if err != nil {
		a.log.Warn("agent: daily totals unavailable; limit not checked", "err", err)
		return nil, false
	}
	if under(tot.Turns, a.cfg.DailyTurns) && under(tot.Tokens, a.cfg.DailyTokens) {
		return nil, false
	}
	a.log.Warn("agent: daily limit reached", "principal", principal, "turns", tot.Turns, "tokens", tot.Tokens)
	alerts, err := a.d.Alerts.Messages(ctx, "alert:daily-limit:"+day.Format("2006-01-02")+":"+principal,
		"Daily limit reached for "+principal+" (turns or tokens); the bot refuses them until tomorrow.")
	if err != nil {
		a.log.Warn("agent: daily limit alert not built", "err", err)
	}
	return alerts, true
}

// GiveUp is the agent's core.GiveUp: an apology to the person and an
// alert to the admins for a message that failed every attempt. A
// scheduled job has nobody to apologise to in its conversation: only the
// admins hear of it.
func (a *Agent) GiveUp(e event.Event, attempts int) []event.Outgoing {
	var out []event.Outgoing
	if e.Kind != event.KindSchedule {
		out = append(out, event.Outgoing{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv,
			Text: "Sorry, something went wrong with that message."})
	}
	alerts, err := a.d.Alerts.Messages(context.Background(), "alert:give-up:"+e.ID,
		"Gave up on event "+e.ID+" in "+e.Conv+" after "+strconv.Itoa(attempts)+" attempts.")
	if err != nil {
		a.log.Warn("node: give-up alert not built", "id", e.ID, "err", err)
	}
	return append(out, alerts...)
}

// under reports whether used is below limit; a zero limit is off.
func under(used, limit int) bool { return limit <= 0 || used < limit }
