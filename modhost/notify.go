package modhost

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/runner"
)

// This file is notify (R-RUN-16, ADR-0066): a module's message for its
// owner is counted per day in the node's time zone and put into the inbox
// as a module.notify event in the owner's private chat; the agent writes
// the node's note into the history and the message into the outbox, with
// no model (ruling 1 of M4c). Over the daily limit nothing is sent and the
// owner gets one node line that day.

// notifyOutcome is what became of a notify call, for the metrics.
type notifyOutcome string

// Notify outcomes.
const (
	notifySent    notifyOutcome = "sent"     // the event went to the inbox
	notifyLimit   notifyOutcome = "limit"    // past the manifest's per_day
	notifyNotHere notifyOutcome = "not_here" // init or health: no run
	notifyFailed  notifyOutcome = "failed"   // the node could not count or put it
)

// code is the answer the module gets for o. A failure of the node's is
// "limit": nothing was sent, and the module cannot tell the two apart.
func (o notifyOutcome) code() runner.NotifyCode {
	switch o {
	case notifySent:
		return runner.NotifySent
	case notifyNotHere:
		return runner.NotifyNotHere
	case notifyLimit, notifyFailed:
	}
	return runner.NotifyLimit
}

// notifyPort is the runner's NotifyPort of one loaded module. The runner
// checks the manifest's grant and cuts the text before the port.
type notifyPort struct {
	h *Host
	l *loaded
}

// Notify implements runner.NotifyPort. Only a run sends (ruling 3 of M4c):
// init and health carry no run and get NotifyNotHere. A message is counted
// before it is put (ruling 11 of M4c); past the manifest's per_day it is
// not put, the owner gets the limit line once that day and the module gets
// NotifyLimit. A message's key is its run and its number in the run, so a
// run delivered again sends nothing twice. The node's failure to count or
// put it is logged and answered NotifyLimit: nothing was sent.
func (p notifyPort) Notify(ctx context.Context, text string) runner.NotifyCode {
	o := p.notify(ctx, text)
	p.h.in.notified(ctx, p.l.name, o)
	return o.code()
}

// notify counts and puts one message, and says what became of it.
func (p notifyPort) notify(ctx context.Context, text string) notifyOutcome {
	r, ok := runOf(ctx)
	if !ok {
		return notifyNotHere
	}
	now := p.h.d.Now()
	day := clock.DayStart(now, p.h.d.Location)
	n, limited, err := p.h.d.Store.CountNotify(ctx, p.l.name, day)
	if err != nil {
		p.h.d.Log.Error("modhost: a module's message was not counted; it is not sent", "module", p.l.name, "err", err)
		return notifyFailed
	}
	if n > perDay(p.l) {
		if !limited {
			p.h.limitLine(ctx, p.l, now, day)
		}
		return notifyLimit
	}
	*r.sent++
	key := "notify:" + p.l.name + ":" + r.ID + ":" + strconv.Itoa(*r.sent)
	if err := p.h.putNotify(ctx, p.l, key, event.ModuleNotify{Text: text, PerDay: perDay(p.l)}, now); err != nil {
		p.h.d.Log.Error("modhost: a module's message was not put", "module", p.l.name, "err", err)
		return notifyFailed
	}
	return notifySent
}

// perDay is l's daily limit of messages; 0 when its manifest grants no
// notify, which the runner refuses before the port.
func perDay(l *loaded) int {
	if n := l.m.Capabilities.Notify; n != nil {
		return n.PerDay
	}
	return 0
}

// limitLine tells l's owner that it reached its limit on day. The day's key
// keeps it to one event and the mark to one put; a failure is logged, and
// the next message over the limit tries again.
func (h *Host) limitLine(ctx context.Context, l *loaded, now, day time.Time) {
	key := "notify:" + l.name + ":limit:" + day.Format(time.DateOnly)
	if err := h.putNotify(ctx, l, key, event.ModuleNotify{Node: event.NotifyLimitLine, PerDay: perDay(l)}, now); err != nil {
		h.d.Log.Error("modhost: the line of a module's daily limit was not put", "module", l.name, "err", err)
		return
	}
	if err := h.d.Store.MarkLimited(context.WithoutCancel(ctx), l.name, day); err != nil {
		h.d.Log.Warn("modhost: a module's daily limit is not marked; its key keeps the line to one", "module", l.name, "err", err)
	}
}

// putNotify puts p for l's owner under key, in their private chat. The put
// does not end with ctx: the message was counted and the run goes on, so a
// shutdown does not lose it.
func (h *Host) putNotify(ctx context.Context, l *loaded, key string, p event.ModuleNotify, now time.Time) error {
	p.Module, p.Version, p.At, p.Principal, p.Transport = l.name, l.version, now.UnixMilli(), l.to.principal, l.to.transport
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return h.d.Inbox.Put(context.WithoutCancel(ctx), event.Event{ID: event.NewID(now), IdemKey: key, Source: event.SourceModule,
		Conv: l.to.conv, Kind: event.KindModuleNotify, Payload: payload, At: now})
}
