package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// overLimit checks the principal's daily turns and tokens. Over a limit it
// returns one alert per admin; their keys repeat for the day, so the outbox
// sends each alert once.
func (a *Agent) overLimit(ctx context.Context, now time.Time, principal, eventID string) ([]event.Outgoing, bool) {
	tot, over := a.totals(ctx, now, principal, eventID)
	if !over {
		return nil, false
	}
	day := clock.DayStart(now, a.cfg.Location)
	a.log.Warn("agent: daily limit reached", "principal", principal, "turns", tot.Turns, "tokens", tot.Tokens)
	alerts, err := a.d.Alerts.Messages(ctx, "alert:daily-limit:"+day.Format("2006-01-02")+":"+principal,
		"Daily limit reached for "+principal+" (turns or tokens); the bot refuses them until tomorrow.")
	if err != nil {
		a.log.Warn("agent: daily limit alert not built", "err", err)
	}
	return alerts, true
}

// over reports whether the principal is past a daily limit. It sends nothing:
// a message waiting behind a turn is checked with it (R-LIVE-1), and the
// alert belongs to that message's own turn. Totals that cannot be read leave
// the limit unchecked.
func (a *Agent) over(ctx context.Context, now time.Time, principal, eventID string) bool {
	_, over := a.totals(ctx, now, principal, eventID)
	return over
}

// totals reads the principal's day and whether it is past a limit.
func (a *Agent) totals(ctx context.Context, now time.Time, principal, eventID string) (turns.Totals, bool) {
	if a.cfg.DailyTurns <= 0 && a.cfg.DailyTokens <= 0 {
		return turns.Totals{}, false
	}
	tot, err := a.d.Turns.Totals(ctx, clock.DayStart(now, a.cfg.Location), principal, eventID)
	if err != nil {
		a.log.Warn("agent: daily totals unavailable; limit not checked", "err", err)
		return turns.Totals{}, false
	}
	return tot, !under(tot.Turns, a.cfg.DailyTurns) || !under(tot.Tokens, a.cfg.DailyTokens)
}

// giveUpText is the apology in the language of the person behind e. GiveUp has no turn, and its
// event may be one that failed to decode: then it is in English.
func (a *Agent) giveUpText(e event.Event) string {
	var who struct {
		From string `json:"from"`
	}
	_ = json.Unmarshal(e.Payload, &who)
	return a.say(context.Background(), &turn{msg: event.Message{From: who.From}}, texts.ReplyGiveUp, nil)
}

// GiveUp is the agent's core.GiveUp: an apology to the person and an
// alert to the admins for a message that failed every attempt. A
// scheduled job, any event of a task's conversation (a command's end) and
// the node's own module events (an escalation, a trigger, a message) have
// nobody to apologise to in their conversation: only the admins hear of it.
func (a *Agent) GiveUp(e event.Event, attempts int) []event.Outgoing {
	var out []event.Outgoing
	if e.Kind != event.KindSchedule && e.Source != event.SourceModule && !strings.HasPrefix(e.Conv, tool.TaskConvPrefix) {
		out = append(out, event.Outgoing{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv,
			Text: a.giveUpText(e)})
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
