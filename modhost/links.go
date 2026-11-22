package modhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// This file is what the host does with the links of the node's file
// (R-BUS-5): it names the links a removal takes, and at start and in
// /status it reports the ones that do not work. Nothing travels over a link
// here; a broken one gets no consumer (R-BUS-6).

// linkReasonKeys are the /status reasons of a broken link.
var linkReasonKeys = map[links.Reason]texts.Key{
	links.ReasonEnd:   texts.StatusReasonLinkEnd,
	links.ReasonKind:  texts.StatusReasonLinkKind,
	links.ReasonShape: texts.StatusReasonLinkShape,
	links.ReasonLoop:  texts.StatusReasonLinkLoop,
	links.ReasonDeep:  texts.StatusReasonLinkDeep,
}

// Links are the links of the node's file that name the module, written
// `a.x → b.y`: what removing the module takes with it.
func (h *Host) Links(_ context.Context, name string) ([]string, error) {
	ls, err := h.d.Settings.Links()
	if err != nil {
		return nil, fmt.Errorf("modhost: %w", err)
	}
	var out []string
	for _, l := range ls {
		if l.Touches(fullName(name)) {
			out = append(out, l.String())
		}
	}
	return out, nil
}

// linkProblems are the links that do not work among the running modules: a
// module that is not running, or one with a port gone or another, counts
// as a missing end.
func (h *Host) linkProblems() ([]links.Problem, error) {
	ls, err := h.d.Settings.Links()
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	h.mu.RLock()
	mods := make(map[string]links.Module, len(h.running))
	for _, l := range h.running {
		mods[l.short] = links.FromManifest(l.m)
	}
	h.mu.RUnlock()
	var out []links.Problem
	for _, p := range links.Check(mods, linksOf(ls)) {
		if p.Link != (links.Link{}) { // a loop of cell triggers alone is no link's
			out = append(out, p)
		}
	}
	return out, nil
}

// linksOf is the links of the file as the links package takes them.
func linksOf(ls []settings.Link) []links.Link {
	out := make([]links.Link, len(ls))
	for i, l := range ls {
		out[i] = links.Link(l)
	}
	return out
}

// checkLinks logs each broken link once, at start, after the modules are
// loaded. A file that cannot be read is logged too, and left to the status.
func (h *Host) checkLinks() {
	ps, err := h.linkProblems()
	if err != nil {
		h.d.Log.Warn("modhost: the links are not checked: the settings cannot be read", "err", err)
		return
	}
	for _, p := range ps {
		h.d.Log.Warn("modhost: a link does not work; it gets no events and no calls", "link", p.Link.String(), "reason", p.Reason, "detail", p.Words())
	}
}

// linkChecks are the /status lines of the broken links, each with the time
// it was first seen broken. It holds h.stateMu.
func (h *Host) linkChecks() []status.Check {
	ps, err := h.linkProblems()
	if err != nil || len(ps) == 0 {
		h.broken = nil
		return nil
	}
	now := h.d.Now()
	seen := map[string]time.Time{}
	out := make([]status.Check, 0, len(ps))
	for _, p := range ps {
		key := p.Words()
		since, ok := h.broken[key]
		if !ok {
			since = now
		}
		seen[key] = since
		out = append(out, linkCheck(p, since))
	}
	h.broken = seen
	return out
}

// linkCheck is the /status line of one problem.
func linkCheck(p links.Problem, since time.Time) status.Check {
	link := p.Link.String()
	return status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusLinkBroken, Reason: linkReasonKeys[p.Reason],
		Vars: texts.Vars{"link": texts.Escape(link), "path": texts.Escape(strings.Join(p.Path, " → ")), "max": strconv.Itoa(links.MaxDepth)}, Since: since}
}
