package builtin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// Limits of one module.install request (ADR-0111).
const (
	maxInstallBuilds = 5
	maxInstallLinks  = 10
)

// installArgs are the arguments of module.install: one build in the old
// form, or one to five builds and the links between their ports and the
// installed modules.
type installArgs struct {
	Build  string        `json:"build"`
	Builds []string      `json:"builds"`
	Links  []installLink `json:"links"`
	Note   string        `json:"note"`
}

// installLink is one link of a request.
type installLink struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// installBuild is one build of a request, loaded and planned.
type installBuild struct {
	sha   string
	build modstore.Build
}

// installRequest is a module.install call read, loaded and checked: the
// builds in the order they install, then the links in the order they are
// written.
type installRequest struct {
	note   string
	builds []installBuild
	links  []settings.Link
}

// requestWindow is what the install question shows and the press shows
// again: a window per build, then the links with their shapes. The press
// installs only when the request, checked again, shows the same.
type requestWindow struct {
	Builds []window    `json:"builds"`
	Links  []shownLink `json:"links,omitempty"`
}

// shownLink is a link of the request as the question shows it.
type shownLink struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Shape string `json:"shape"`
}

// digests are the digests a call names, in order: build alone, or builds.
func (a installArgs) digests() ([]string, error) {
	switch {
	case a.Build != "" && len(a.Builds) > 0:
		return nil, errors.New("give build (one digest) or builds (one to five), not both")
	case a.Build != "":
		return []string{a.Build}, nil
	case len(a.Builds) == 0:
		return nil, errors.New("builds is one to five digests, as module.build answered them")
	case len(a.Builds) > maxInstallBuilds:
		return nil, fmt.Errorf("at most %d builds in one install; install the rest in another call", maxInstallBuilds)
	}
	return a.Builds, nil
}

// request reads call c and loads its builds: each build's files are the ones
// its digest names, no build is running, the node's plan accepts it, and
// each module comes once. A refusal is the node's words, for the model.
func (t *ModuleInstall) request(ctx context.Context, c tool.Call) (installRequest, []planned, error) {
	var a installArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return installRequest{}, nil, fmt.Errorf("arguments: %w", err)
	}
	shas, err := a.digests()
	if err != nil {
		return installRequest{}, nil, err
	}
	if len(a.Links) > maxInstallLinks {
		return installRequest{}, nil, fmt.Errorf("at most %d links in one install", maxInstallLinks)
	}
	req := installRequest{note: oneLine(a.Note)}
	var ps []planned
	seen := map[string]bool{}
	for _, sha := range shas {
		p, err := t.loadPlanned(ctx, sha)
		if err != nil {
			return installRequest{}, nil, err
		}
		if seen[p.b.Module] {
			return installRequest{}, nil, fmt.Errorf("%s comes twice in this install: one build for each module", p.b.Module)
		}
		seen[p.b.Module] = true
		req.builds = append(req.builds, installBuild{sha: sha, build: p.b})
		ps = append(ps, p)
	}
	for _, l := range a.Links {
		link := settings.Link{From: strings.TrimSpace(l.From), To: strings.TrimSpace(l.To)}
		if slices.Contains(req.links, link) {
			return installRequest{}, nil, fmt.Errorf("the link %s comes twice in this install", link)
		}
		req.links = append(req.links, link)
	}
	return req, ps, nil
}

// trialOf is the trial report kept for a build, if there is one.
type trialOf struct {
	report modtrial.Report
	ok     bool
}

// planned is a build of a request with what the question needs of it.
type planned struct {
	b     modstore.Build
	plan  modstore.Plan
	m     runner.Manifest
	win   window
	trial trialOf
}

// loadPlanned loads the build named sha and plans it.
func (t *ModuleInstall) loadPlanned(ctx context.Context, sha string) (planned, error) {
	b, err := t.m.loadBuild(sha)
	if err != nil {
		return planned{}, err
	}
	if t.m.Queue.Building(modstore.ShortName(b.Module)) {
		return planned{}, errBuildRunning
	}
	p, err := t.m.plan(ctx, b)
	if err != nil {
		return planned{}, err
	}
	if p.Refusal != "" {
		return planned{}, fmt.Errorf("the node refuses the build of %s: %s (module.build shows the detail)", modstore.ShortName(b.Module), p.Refusal)
	}
	m, err := manifestOf(b)
	if err != nil {
		return planned{}, err
	}
	r, ok, err := t.m.loadTrial(p.SHA256)
	if err != nil {
		return planned{}, err
	}
	return planned{b: b, plan: p, m: m, trial: trialOf{r, ok}}, nil
}

// checkLinks checks each link of the request as the link tools would, with
// the builds installed: the active modules, the builds in place of the
// versions they replace, and the links of the file, then those of the
// request before it. It returns the links with their shapes.
func (t *ModuleInstall) checkLinks(ctx context.Context, req installRequest, ps []planned) ([]shownLink, error) {
	if len(req.links) == 0 {
		return nil, nil
	}
	mods, err := t.m.Links.Modules(ctx)
	if err != nil {
		return nil, fmt.Errorf("the modules' ports: %w", err)
	}
	mods = maps.Clone(mods)
	if mods == nil {
		mods = map[string]links.Module{}
	}
	for _, p := range ps {
		mods[modstore.ShortName(p.b.Module)] = links.FromManifest(p.m)
	}
	existing, err := t.m.Links.File.Links()
	if err != nil {
		return nil, fmt.Errorf("the links: %w", err)
	}
	var out []shownLink
	for _, l := range req.links {
		if err := refuseLink(mods, existing, l); err != nil {
			return nil, err
		}
		existing = append(slices.Clone(existing), l)
		out = append(out, shownLink{From: l.From, To: l.To, Shape: links.Describe(mods, links.Link(l))})
	}
	return out, nil
}

// prepare reads the call, loads and plans its builds, checks its links and
// words the window: a refusal of any of them is the node's words and no
// window.
func (t *ModuleInstall) prepare(ctx context.Context, c tool.Call) (installRequest, requestWindow, error) {
	req, ps, err := t.request(ctx, c)
	if err != nil {
		return installRequest{}, requestWindow{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	shown, err := t.checkLinks(ctx, req, ps)
	if err != nil {
		return installRequest{}, requestWindow{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	for i := range ps {
		ps[i].win = windowOf(ps[i].b, ps[i].plan, ps[i].m, t.m.listener())
		ps[i].win.Outside = !t.m.ownSource(modstore.ShortName(ps[i].b.Module))
	}
	rw := requestWindow{Links: shown}
	rw.Builds = t.trials(c.Speak, ps, rw)
	return req, rw, nil
}

// trials gives each window its trial section: the whole section for one
// module, a count for several, since the question holds them all within
// tool.MaxQuestionRunes.
func (t *ModuleInstall) trials(sp texts.Speaker, ps []planned, rw requestWindow) []window {
	out := make([]window, len(ps))
	for i, p := range ps {
		if len(ps) > 1 {
			out[i] = compactTrial(sp, p.win, p.trial.report, p.trial.ok)
			continue
		}
		out[i] = withTrial(sp, p.win, p.trial.report, p.trial.ok, utf8.RuneCountInString(linksText(sp, rw.Links)))
	}
	return out
}

// requestText is the install question: the window of the one build, or, for
// several builds or links, a line saying one press does all, a compact window
// per build when there are several, then the links with the shape that
// crosses each.
func requestText(sp texts.Speaker, rw requestWindow) string {
	if len(rw.Builds) == 1 && len(rw.Links) == 0 {
		return windowText(sp, rw.Builds[0])
	}
	var parts []string
	names := make([]string, len(rw.Builds))
	for i, w := range rw.Builds {
		names[i] = modstore.LocalPrefix + item(modstore.ShortName(w.Module))
	}
	parts = append(parts, sp.Text(texts.AskModuleRequest, texts.Vars{varItems: strings.Join(names, ", ")}))
	for _, w := range rw.Builds {
		parts = append(parts, blockText(sp, w, true))
	}
	if l := linksText(sp, rw.Links); l != "" {
		parts = append(parts, l)
	}
	return strings.Join(parts, "\n\n")
}

// linksText is the links section of the question, "" for none.
func linksText(sp texts.Speaker, ls []shownLink) string {
	if len(ls) == 0 {
		return ""
	}
	lines := []string{sp.Text(texts.AskModuleLinks, nil)}
	for _, l := range ls {
		lines = append(lines, sp.Text(texts.AskModuleLink, texts.Vars{varFrom: texts.Escape(l.From), varTo: texts.Escape(l.To), "shape": texts.Escape(l.Shape)}))
	}
	return strings.Join(lines, "\n")
}

// outcome of a request's press, in the words the model reads: what was
// installed and linked, what failed and why, what was not tried.
type outcome struct {
	done   []string
	failed string
	rest   []string
}

// failure is the error that answers a press that stopped at an item.
func (o outcome) failure() error {
	var b strings.Builder
	if len(o.done) > 0 {
		b.WriteString("done: " + strings.Join(o.done, "; ") + ". ")
	}
	b.WriteString("failed: " + o.failed + ".")
	if len(o.rest) > 0 {
		b.WriteString(" Not tried: " + strings.Join(o.rest, "; ") + ".")
	}
	return fmt.Errorf("%s: %s", ModuleInstallTool, b.String())
}
