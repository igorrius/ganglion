package modhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// This file is what the host does with the links of the node's file
// (R-BUS-5): it names the links a removal takes, and at start and in
// /status it reports the ones that do not work. A broken link gets no
// consumer (R-BUS-6, linkconsumers.go).

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

// linkState is the links of the file, the active local modules' ports by
// short name (installed ones, running or not) and the problems among them.
// A module turned off or stopped keeps its links: what it is sent waits for
// it (R-BUS-6). A module with no active version, or one with a port gone or
// another, is a missing end.
func (h *Host) linkState(ctx context.Context) ([]settings.Link, map[string]links.Module, []links.Problem, error) {
	ls, err := h.d.Settings.Links()
	if err != nil || len(ls) == 0 {
		return nil, nil, nil, err
	}
	actives, err := h.d.Store.ActiveLocal(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("modhost: %w", err)
	}
	mods := make(map[string]links.Module, len(actives))
	for _, a := range actives {
		m, err := runner.ParseManifest(a.Manifest)
		if err != nil {
			continue // it does not run either
		}
		mods[modstore.ShortName(a.Module)] = links.FromManifest(m)
	}
	var out []links.Problem
	for _, p := range links.Check(mods, linksOf(ls)) {
		if p.Link != (links.Link{}) { // a loop of cell triggers alone is no link's
			out = append(out, p)
		}
	}
	return ls, mods, out, nil
}

// linkProblems are the links that do not work among the active local
// modules.
func (h *Host) linkProblems(ctx context.Context) ([]links.Problem, error) {
	_, _, ps, err := h.linkState(ctx)
	return ps, err
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
func (h *Host) checkLinks(ctx context.Context) {
	ps, err := h.linkProblems(ctx)
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
func (h *Host) linkChecks(ctx context.Context) []status.Check {
	ps, err := h.linkProblems(ctx)
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
