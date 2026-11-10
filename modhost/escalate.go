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
	escalationHeld   escalationOutcome = "held"   // an episode of the kind is open already
	escalationFailed escalationOutcome = "failed" // the inbox refused it
)

// escalates reports whether a failure of kind calls the model: the
// manifest's escalate kinds, and always a contract break and a crash loop
// (design section 8.4).
func (l *loaded) escalates(kind Kind) bool {
	return kind == KindFormat || kind == KindCrashloop || l.escalate[kind]
}

// escalate puts the event that makes kind a turn in the module's admin's
// private chat (ruling 10 of the M4b plan), once per kind for the module's
// active version until a successful run, a new version, a rollback or an
// "on" closes the episode (owner ruling of 2026-11-10). The durable store is
// the truth, so a restart in the middle of an episode does not escalate
// again; the host's memory only saves asking it, and the inbox key
// "module:<name>:<kind>:<sha>:<opened ms>" still keeps one event of an
// episode. detail is the node's words; the module's text never goes in
// (R-RUN-14). A failure to put it is logged: a lost escalation never fails
// a run.
func (h *Host) escalate(ctx context.Context, l *loaded, kind Kind, detail string) {
	if !l.escalates(kind) {
		return
	}
	h.episodeMu.Lock()
	defer h.episodeMu.Unlock()
	if h.episodes[l.name][kind] == l.sha {
		h.in.escalated(ctx, l.name, kind, escalationHeld)
		return
	}
	now := h.d.Now()
	opened, err := h.d.Store.OpenEpisode(ctx, l.name, string(kind), l.sha, now)
	switch {
	case err != nil: // better silent than a flood: the next failure tries again
		h.in.escalated(ctx, l.name, kind, escalationFailed)
		h.d.Log.Error("modhost: an escalation episode cannot be opened", "module", l.name, "kind", string(kind), "err", err)
		return
	case !opened:
		h.noteEpisode(l.name, kind, l.sha)
		h.in.escalated(ctx, l.name, kind, escalationHeld)
		return
	}
	h.noteEpisode(l.name, kind, l.sha)
	key := fmt.Sprintf("module:%s:%s:%s:%d", l.name, kind, l.sha, now.UnixMilli())
	payload, err := json.Marshal(event.Module{Module: l.name, Version: l.version, Kind: string(kind), Detail: detail,
		At: now.UnixMilli(), Principal: l.to.principal, Transport: l.to.transport})
	if err == nil {
		err = h.d.Inbox.Put(ctx, event.Event{ID: event.NewID(now), IdemKey: key, Source: event.SourceModule, Conv: l.to.conv,
			Kind: event.KindModule, Payload: payload, At: now})
	}
	if err != nil {
		h.forgetEpisode(ctx, l.name, kind)
		h.in.escalated(ctx, l.name, kind, escalationFailed)
		h.d.Log.Error("modhost: an escalation was not put", "module", l.name, "kind", string(kind), "err", err)
		return
	}
	h.in.escalated(ctx, l.name, kind, escalationPut)
}

// recovered closes the open episodes of module after a successful run: the
// next failure of any kind escalates again. A module known to have none
// costs nothing.
func (h *Host) recovered(ctx context.Context, module string) {
	h.episodeMu.Lock()
	defer h.episodeMu.Unlock()
	if !h.clean[module] {
		h.clearLocked(ctx, module)
	}
}

// clearEpisodes closes every open episode of module: it recovered, or a new
// version, a rollback or an "on" gave it a fresh start.
func (h *Host) clearEpisodes(ctx context.Context, module string) {
	h.episodeMu.Lock()
	defer h.episodeMu.Unlock()
	h.clearLocked(ctx, module)
}

// clearLocked is clearEpisodes with episodeMu held: the store and the cache
// change together, so a concurrent failure never sees one without the other.
func (h *Host) clearLocked(ctx context.Context, module string) {
	if err := h.d.Store.ClearEpisodes(ctx, module); err != nil {
		h.d.Log.Error("modhost: escalation episodes cannot be closed", "module", module, "err", err)
		return
	}
	delete(h.episodes, module)
	h.clean[module] = true
}

// noteEpisode remembers an open episode, so the store is not asked again.
// It holds episodeMu.
func (h *Host) noteEpisode(module string, kind Kind, sha string) {
	if h.episodes[module] == nil {
		h.episodes[module] = map[Kind]string{}
	}
	h.episodes[module][kind] = sha
	delete(h.clean, module)
}

// forgetEpisode takes back an episode whose event was not put, so the next
// failure tries again. It holds episodeMu.
func (h *Host) forgetEpisode(ctx context.Context, module string, kind Kind) {
	delete(h.episodes[module], kind)
	if err := h.d.Store.CloseEpisode(ctx, module, string(kind)); err != nil {
		h.d.Log.Error("modhost: an escalation episode cannot be taken back", "module", module, "kind", string(kind), "err", err)
	}
}
