package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
)

// This file is notify (R-RUN-16, R-BUS-7, ADR-0066): a module's message is
// counted per day in the node's time zone and published once to the NOTIFY
// stream. A durable consumer in the core hands each message to Deliver,
// which puts one module.notify event per destination into the inbox (the
// owner's private chat when the module's line names none); the agent
// writes the node's note into the history and the message into the outbox,
// with no model (ruling 1 of M4c). The inbox event stays the delivery
// step because the history note is session state the agent writes in order
// with its turns. Over the daily limit nothing is published and the owner
// gets one node line that day, put into the inbox directly.

// Publisher is the bus as notify needs it: it publishes one message to a
// subject and returns when the bus has stored it. A message with an id the
// stream already holds is dropped by the bus and is not an error.
// bus/natsbus implements it; headers are the names of package bus.
type Publisher interface {
	Publish(ctx context.Context, subject, id string, headers map[string]string, payload []byte) error
}

// fromNode names the producer of a notice in its Ganglion-From header:
// this node's core, then the module.
const fromNode = "core"

// notifyOutcome is what became of a notify call, for the metrics.
type notifyOutcome string

// Notify outcomes.
const (
	notifySent    notifyOutcome = "sent"     // the bus stored the message
	notifyLimit   notifyOutcome = "limit"    // past the manifest's per_day
	notifyNotHere notifyOutcome = "not_here" // init or health: no run
	notifyFailed  notifyOutcome = "failed"   // the node could not count or publish it
	// notifyNoDestination is a name of the module's notify_to that
	// resolved to nothing when the message was delivered: that copy was
	// not sent.
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
// publish it is logged and answered NotifyLimit: nothing was sent. The
// answer says the message was stored, not that it reached a chat.
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
	return p.h.publish(ctx, p.l, key, event.ModuleNotify{Module: p.l.name, Version: p.l.version, Text: text, PerDay: perDay(p.l),
		At: now.UnixMilli(), Run: r.ID})
}

// publish stores one message of l in the NOTIFY stream under key, once for
// all its destinations. The publish does not end with ctx: the message was
// counted and the run goes on.
func (h *Host) publish(ctx context.Context, l *loaded, key string, p event.ModuleNotify) notifyOutcome {
	subject, err := bus.Notify(l.short)
	if err == nil {
		var payload []byte
		if payload, err = json.Marshal(p); err == nil {
			headers := map[string]string{bus.HeaderFrom: fromNode + "/" + l.name, bus.HeaderRun: p.Run}
			err = h.d.Publisher.Publish(context.WithoutCancel(ctx), subject, key, headers, payload)
		}
	}
	if err != nil {
		h.d.Log.Error("modhost: a module's message was not published", "module", l.name, "err", err)
		return notifyFailed
	}
	return notifySent
}

// deliveryOutcome is what became of a message the consumer handed to
// Deliver, for the metrics.
type deliveryOutcome string

// Delivery outcomes.
const (
	deliveryDone   deliveryOutcome = "delivered" // every copy was put, or named nothing that resolves
	deliveryGone   deliveryOutcome = "gone"      // the module is no longer loaded; acked, not sent
	deliveryFailed deliveryOutcome = "failed"    // a copy was not put; the bus delivers the message again
	deliveryBad    deliveryOutcome = "bad"       // not a notice; acked, not sent
)

// Deliver is the handler of the core's consumer of the NOTIFY stream (the
// natsbus consumer takes it as its handler): it puts the message's copies,
// one for every destination its module's line names now. A nil return acks
// the message; an error has the bus deliver it again, which puts nothing
// twice because every copy's key is the message's id and a destination.
// A module no longer loaded, and a payload that is no notice, are logged
// and acked: nothing could ever deliver them.
func (h *Host) Deliver(ctx context.Context, id string, payload []byte) error {
	var p event.ModuleNotify
	if err := json.Unmarshal(payload, &p); err != nil || p.Module == "" {
		h.d.Log.Error("modhost: a message of NOTIFY is not a module's notice; it is dropped", "id", id, "err", err)
		h.in.delivered(ctx, "", deliveryBad)
		return nil
	}
	h.mu.RLock()
	l, ok := h.running[p.Module]
	h.mu.RUnlock()
	if !ok {
		h.d.Log.Warn("modhost: a module's message arrived after the module was gone; it is not sent", "module", p.Module, "id", id)
		h.in.delivered(ctx, p.Module, deliveryGone)
		return nil
	}
	if err := h.deliver(ctx, l, id, p, h.d.Now()); err != nil {
		h.in.delivered(ctx, p.Module, deliveryFailed)
		return err
	}
	h.in.delivered(ctx, p.Module, deliveryDone)
	return nil
}

// deliver puts one message of l for every destination its line names, the
// owner alone when it names none (R-RUN-18). The message was counted once,
// whatever the number of destinations. A name that resolves to nothing, or
// cannot be resolved now, is logged and counted, and the other copies still
// go. A copy that cannot be put fails the delivery after the others were
// tried, so the message comes again and only the missing copies are new.
func (h *Host) deliver(ctx context.Context, l *loaded, key string, p event.ModuleNotify, now time.Time) error {
	names := l.notifyTo
	if len(names) == 0 {
		names = []string{settings.DestinationOwner}
	}
	var errs []error
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
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	p.Module, p.Version, p.Principal, p.Transport = l.name, l.version, l.to.principal, to.transport
	if p.At == 0 { // a message keeps the time its module wrote it
		p.At = now.UnixMilli()
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return h.d.Inbox.Put(context.WithoutCancel(ctx), event.Event{ID: event.NewID(now), IdemKey: key, Source: event.SourceModule,
		Conv: to.conv, Kind: event.KindModuleNotify, Payload: payload, At: now})
}
