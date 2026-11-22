package modhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
)

// loaded is a running module.
type loaded struct {
	name, short, sha, version string
	m                         runner.Manifest
	mod                       *runner.Module
	given                     *given       // the secret values it was handed, hidden from its log lines
	log                       *slog.Logger // for the module's texts, through given (moduleLog)
	to                        recipient
	notifyTo                  []string      // the destinations of its messages; none is the owner alone (R-RUN-18)
	escalate                  map[Kind]bool // the manifest's escalate kinds
	tools                     []tool.Handler
}

// recipient is the admin a module belongs to: who installed it, or the
// first admin with a private chat when they are no longer one.
type recipient struct {
	principal, transport, conv string
}

// Reason is why a module that should run does not: a closed set, shown in
// /status by its texts key.
type Reason string

// Reasons.
const (
	ReasonLoad      Reason = "load"      // the manifest, the wasm or a cron line did not load
	ReasonCells     Reason = "cells"     // a cell it reports belongs to another module
	ReasonInit      Reason = "init"      // its init trigger failed
	ReasonNoAdmin   Reason = "no_admin"  // no admin with a private chat to own it
	ReasonCrashloop Reason = "crashloop" // five failures within an hour
)

// words is the reason in the node's words, for an error.
func (r Reason) words() string {
	switch r {
	case ReasonLoad:
		return "it could not be loaded"
	case ReasonCells:
		return "a cell it reports belongs to another module"
	case ReasonInit:
		return "its init failed"
	case ReasonNoAdmin:
		return "no admin with a private chat owns it"
	case ReasonCrashloop:
		return "it failed 5 times within an hour"
	}
	return string(r)
}

// stop is a module that is not running and why, since when.
type stop struct {
	reason Reason
	kind   Kind // the init's failure kind, for ReasonInit
	since  time.Time
}

// prepare makes a running module of a: its manifest, its owner, its config,
// its cells, its runner, its init and its jobs. A module that cannot start
// is a stop with its reason; an error is the node's own failure.
func (h *Host) prepare(ctx context.Context, a modstore.Active, s settings.Module) (*loaded, stop, error) {
	now := h.d.Now()
	m, err := runner.ParseManifest(a.Manifest)
	if err != nil || !toolsDeclareOutput(m) {
		h.d.Log.Warn("modhost: the manifest does not load", "module", a.Module, "err", err)
		return nil, stop{reason: ReasonLoad, since: now}, nil
	}
	to, ok, err := h.owner(ctx, a)
	if err != nil || !ok {
		return nil, stop{reason: ReasonNoAdmin, since: now}, err
	}
	m.ConfigValues = configValues(m, s.Config)
	l := &loaded{name: a.Module, short: modstore.ShortName(a.Module), sha: a.SHA256, version: strconv.Itoa(m.Version), m: m, to: to,
		escalate: escalations(m), given: h.newGiven(a.Module, m), notifyTo: slices.Clone(s.NotifyTo)}
	l.log = h.moduleLog(l.given)
	if st, err := h.defineCells(ctx, l); err != nil || st.reason != "" {
		return nil, st, err
	}
	if l.mod, err = runner.Load(ctx, a.Wasm, m, runner.Deps{Secrets: h.secret(a.Module, l.given), State: h.d.State,
		Cells: cellPort{cells: h.d.Cells, module: l.name, prefix: l.short}, Notify: notifyPort{h: h, l: l}, Emit: emitPort{h: h, l: l}, Prefix: l.short, Logger: l.log, OnCall: onCall}); err != nil {
		h.d.Log.Warn("modhost: the module does not load", "module", a.Module, "err", err)
		return nil, stop{reason: ReasonLoad, since: now}, nil
	}
	if st := h.init(ctx, l); st.reason != "" {
		_ = l.mod.Close(ctx)
		return nil, st, nil
	}
	if err := h.ensureJobs(ctx, l); err != nil {
		_ = l.mod.Close(ctx)
		h.d.Log.Warn("modhost: the module's cron jobs are not set", "module", a.Module, "err", err)
		return nil, stop{reason: ReasonLoad, since: now}, nil
	}
	l.tools = h.toolsOf(l)
	return l, stop{}, nil
}

// toolsDeclareOutput reports whether every tool trigger declares the shape
// of its answer: an answer with none would reach the model unchecked.
func toolsDeclareOutput(m runner.Manifest) bool {
	return !slices.ContainsFunc(m.Triggers, func(t runner.Trigger) bool { return t.Tool != "" && t.Output == nil })
}

// owner is the admin the module belongs to: who installed its active
// version when they are still an admin with a private chat, else the first
// admin with one; false when there is none.
func (h *Host) owner(ctx context.Context, a modstore.Active) (recipient, bool, error) {
	admins, err := h.d.Admins.List(ctx)
	if err != nil {
		return recipient{}, false, fmt.Errorf("modhost: %w", err)
	}
	entries, err := h.d.Store.Journal(ctx, a.Module)
	if err != nil {
		return recipient{}, false, fmt.Errorf("modhost: %w", err)
	}
	for _, e := range entries {
		if e.SHA256 == a.SHA256 && slices.Contains(admins, e.By) {
			admins = append([]string{e.By}, admins...)
			break
		}
	}
	for _, p := range admins {
		if transport, conv, ok := h.d.Admins.Private(p); ok {
			return recipient{principal: p, transport: transport, conv: conv}, true, nil
		}
	}
	return recipient{}, false, nil
}

// configValues are the values of the keys the manifest declares: the
// settings' value, else the declared default. Other keys never reach the
// module.
func configValues(m runner.Manifest, cfg map[string]any) map[string]string {
	out := make(map[string]string, len(m.Config))
	for key, k := range m.Config {
		if v, ok := cfg[key]; ok {
			out[key] = fmt.Sprint(v)
		} else if k.Default != "" {
			out[key] = k.Default
		}
	}
	return out
}

// escalations are the kinds the manifest's escalate list names.
func escalations(m runner.Manifest) map[Kind]bool {
	out := make(map[Kind]bool, len(m.Escalate))
	for _, e := range m.Escalate {
		out[Kind(e.On)] = true
	}
	return out
}

// secret is the runner's secret lookup for module: the module's own source
// (its scope of the secret store, then the environment; ADR-0077), read on
// every call (a value set while the module runs applies at once); a secret that cannot
// be read is logged by name and is not set, and one of the node's own is
// never set. Every value handed out goes to g first, so the module's logger
// hides it before the module can echo it. The runner asks only for the
// names the manifest grants.
func (h *Host) secret(module string, g *given) func(string) (string, bool) {
	src := h.d.Secrets(module)
	return func(name string) (string, bool) {
		if modstore.NodeSecret(h.d.NodeSecrets, name) {
			h.d.Log.Warn("modhost: a module asked for one of the node's own secrets; it is not given", "module", module, "secret", name)
			return "", false
		}
		v, err := src.Secret(name)
		if err != nil {
			h.d.Log.Warn("modhost: a module's secret cannot be read", "module", module, "secret", name, "err", err)
			return "", false
		}
		g.add(name, v)
		return v, v != ""
	}
}

// defineCells creates the module's cells under its own prefix, owned by it,
// or takes them back when they are ownerless (ADR-0062). A cell of another
// module stops it.
func (h *Host) defineCells(ctx context.Context, l *loaded) (stop, error) {
	names := make([]string, 0, len(l.m.Cells.Report))
	for name := range l.m.Cells.Report {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		d := l.m.Cells.Report[name]
		err := h.d.Cells.DefineOwned(ctx, cells.OwnedDef{ID: l.short + "." + name, Owner: l.name, About: d.About, Kind: cells.KindSensor})
		switch {
		case errors.Is(err, cells.ErrOwned):
			h.d.Log.Warn("modhost: a module's cell belongs to another module", "module", l.name, "cell", l.short+"."+name)
			return stop{reason: ReasonCells, since: h.d.Now()}, nil
		case err != nil:
			return stop{reason: ReasonLoad, since: h.d.Now()}, fmt.Errorf("modhost: cell %s.%s: %w", l.short, name, err)
		}
	}
	return stop{}, nil
}

// init runs the module's init trigger. A failure is counted and escalated
// as a call's would be, and keeps the module stopped (design section 8.2).
func (h *Host) init(ctx context.Context, l *loaded) stop {
	err := l.mod.Init(ctx)
	if err == nil {
		return stop{}
	}
	o := initOutcome(err)
	h.d.Store.Count(ctx, l.name, l.sha, o.callResult())
	l.log.Warn("modhost: the module's init failed", "module", l.name, "version", l.version, "kind", o.kind,
		"err", l.given.logged(err.Error()))
	h.escalate(ctx, l, o.kind, withHint(o.detail, o, l.short))
	return stop{reason: ReasonInit, kind: o.kind, since: h.d.Now()}
}

// moduleJobID is the id of a module's n-th cron job.
func moduleJobID(module string, n int) string {
	return "module:" + module + ":" + strconv.Itoa(n)
}

// ensureJobs replaces the module's cron jobs with one per cron trigger,
// owned by its admin in their private chat (ruling 9 of the M4b plan).
func (h *Host) ensureJobs(ctx context.Context, l *loaded) error {
	if err := h.d.Jobs.DeleteModule(ctx, l.name); err != nil {
		return err
	}
	n := 0
	for _, t := range l.m.Triggers {
		if t.Cron == "" {
			continue
		}
		j := schedule.Job{ID: moduleJobID(l.name, n), Owner: l.to.principal, Conv: l.to.conv, Transport: l.to.transport,
			Kind: schedule.KindModule, Cron: t.Cron, Text: l.name, System: true, Created: h.d.Now()}
		if err := h.d.Jobs.EnsureModule(ctx, j); err != nil {
			return err
		}
		n++
	}
	return nil
}
