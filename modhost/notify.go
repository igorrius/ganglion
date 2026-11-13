package modhost

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
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
	// notifyNoDestination is a name of the module's notify_to that
	// resolved to nothing: that copy was not sent.
	notifyNoDestination notifyOutcome = "no_destination"
)

// code is the answer the module gets for o. A failure of the node's is
// "limit": nothing was sent, and the module cannot tell the two apart.
func (o notifyOutcome) code() runner.NotifyCode {
	switch o {
	case notifySent:
		return runner.NotifySent
	case notifyNotHere:
		return runner.NotifyNotHere
	case notifyLimit, notifyFailed, notifyNoDestination:
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
	return p.h.deliver(ctx, p.l, key, event.ModuleNotify{Text: text, PerDay: perDay(p.l)}, now)
}

// deliver puts one message of l for every destination its line names, the
// owner alone when it names none (R-RUN-18). The message was counted once,
// whatever the number of destinations. It is sent when at least one copy
// was put; a name that resolves to nothing, or cannot be resolved now, is
// logged and counted, and the other copies still go.
func (h *Host) deliver(ctx context.Context, l *loaded, key string, p event.ModuleNotify, now time.Time) notifyOutcome {
	names := l.notifyTo
	if len(names) == 0 {
		names = []string{settings.DestinationOwner}
	}
	out := notifyFailed
	for _, name := range names {
		to, toKey, ok := h.destination(ctx, l, key, name)
		if !ok {
			continue
		}
		copyOf := p
		if name != settings.DestinationOwner {
			copyOf.To = name
		}
		if err := h.put(ctx, l, to, toKey, copyOf, now); err != nil {
			h.d.Log.Error("modhost: a module's message was not put", "module", l.name, "to", name, "err", err)
			continue
		}
		out = notifySent
	}
	return out
}

// destination is where the copy of a message for name goes and its key.
// The owner's copy keeps the message's own key, as before destinations;
// another's adds the name.
func (h *Host) destination(ctx context.Context, l *loaded, key, name string) (recipient, string, bool) {
	if name == settings.DestinationOwner {
		return l.to, key, true
	}
	transport, conv, ok, err := h.d.Destinations(name)
	if err != nil || !ok {
		h.d.Log.Warn("modhost: a destination of a module's messages does not resolve; that copy is not sent", "module", l.name, "to", name, "err", err)
		h.in.notified(ctx, l.name, notifyNoDestination)
		return recipient{}, "", false
	}
	return recipient{principal: l.to.principal, transport: transport, conv: conv}, key + ":to:" + name, true
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

// putNotify puts p for l's owner under key, in their private chat: the
// node's own lines about a module go to the owner only (R-RUN-18).
func (h *Host) putNotify(ctx context.Context, l *loaded, key string, p event.ModuleNotify, now time.Time) error {
	return h.put(ctx, l, l.to, key, p, now)
}

// put puts p under key in the conversation of to. The principal stays the
// module's owner, who answers for it wherever it goes. The put does not
// end with ctx: the message was counted and the run goes on, so a shutdown
// does not lose it.
func (h *Host) put(ctx context.Context, l *loaded, to recipient, key string, p event.ModuleNotify, now time.Time) error {
	p.Module, p.Version, p.At, p.Principal, p.Transport = l.name, l.version, now.UnixMilli(), l.to.principal, to.transport
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return h.d.Inbox.Put(context.WithoutCancel(ctx), event.Event{ID: event.NewID(now), IdemKey: key, Source: event.SourceModule,
		Conv: to.conv, Kind: event.KindModuleNotify, Payload: payload, At: now})
}
