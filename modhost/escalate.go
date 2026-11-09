package modhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/igorrius/ganglion/event"
)

// escalationOutcome is what became of an escalation, for the metrics.
type escalationOutcome string

// Escalation outcomes.
const (
	escalationPut    escalationOutcome = "put"    // the event went to the inbox
	escalationHeld   escalationOutcome = "held"   // one of the kind went this hour already
	escalationFailed escalationOutcome = "failed" // the inbox refused it
)

// hourLayout names the hour of an escalation's key, in UTC.
const hourLayout = "2006-01-02T15"

// escalates reports whether a failure of kind calls the model: the
// manifest's escalate kinds, and always a contract break and a crash loop
// (design section 8.4).
func (l *loaded) escalates(kind Kind) bool {
	return kind == KindFormat || kind == KindCrashloop || l.escalate[kind]
}

// escalate puts the event that makes kind a turn in the module's admin's
// private chat (ruling 10 of the M4b plan), at most one of a kind for a
// module in an hour: the key "module:<name>:<kind>:<hour>" makes the inbox
// keep one, and the host does not even try a second. detail is the node's
// words; the module's text never goes in (R-RUN-14). A failure to put it is
// logged: a lost escalation never fails a run.
func (h *Host) escalate(ctx context.Context, l *loaded, kind Kind, detail string) {
	if !l.escalates(kind) {
		return
	}
	now := h.d.Now()
	hour := now.UTC().Format(hourLayout)
	key := fmt.Sprintf("module:%s:%s:%s", l.name, kind, hour)
	if !h.markSent(key, hour) {
		h.in.escalated(ctx, l.name, kind, escalationHeld)
		return
	}
	payload, err := json.Marshal(event.Module{Module: l.name, Version: l.version, Kind: string(kind), Detail: detail,
		At: now.UnixMilli(), Principal: l.to.principal, Transport: l.to.transport})
	if err == nil {
		err = h.d.Inbox.Put(ctx, event.Event{ID: event.NewID(now), IdemKey: key, Source: event.SourceModule, Conv: l.to.conv,
			Kind: event.KindModule, Payload: payload, At: now})
	}
	if err != nil {
		h.unmarkSent(key)
		h.in.escalated(ctx, l.name, kind, escalationFailed)
		h.d.Log.Error("modhost: an escalation was not put", "module", l.name, "kind", string(kind), "err", err)
		return
	}
	h.in.escalated(ctx, l.name, kind, escalationPut)
}

// markSent records key for hour and reports whether it is new; keys of
// earlier hours are forgotten.
func (h *Host) markSent(key, hour string) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	for k, at := range h.sent {
		if at != hour {
			delete(h.sent, k)
		}
	}
	if _, ok := h.sent[key]; ok {
		return false
	}
	h.sent[key] = hour
	return true
}

// unmarkSent forgets key, so the next failure tries again.
func (h *Host) unmarkSent(key string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	delete(h.sent, key)
}
