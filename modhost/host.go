// Package modhost hosts the owner's modules in the node (R-RUN-11..14,
// ADR-0061, ADR-0062): it loads the active local versions, defines the cells
// they own, keeps their cron jobs, serves their tools to the agent, checks
// every answer against the manifest's contract, counts failures, disables a
// module that fails five times within an hour and puts the escalations the
// agent answers. It is the only place that knows a module, its cells, its
// jobs and its tools together; only config imports it.
package modhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
)

// Store is the module store's side the host needs (modstore.Store).
type Store interface {
	ActiveLocal(ctx context.Context) ([]modstore.Active, error)
	Count(ctx context.Context, module, sha string, r modstore.CallResult)
	Journal(ctx context.Context, module string) ([]modstore.Entry, error)
	List(ctx context.Context) ([]modstore.Version, error)
	Rollback(ctx context.Context, module string) (modstore.Outcome, error)
	RemoveLocal(ctx context.Context, module string) error
	RemovedLocal(ctx context.Context) ([]string, error)
}

// Jobs keeps the modules' cron jobs (schedule.Store).
type Jobs interface {
	EnsureModule(ctx context.Context, j schedule.Job) error
	DeleteModule(ctx context.Context, module string) error
}

// Inbox takes the escalation events; an IdemKey already stored is a no-op
// (the queue).
type Inbox interface {
	Put(ctx context.Context, e event.Event) error
}

// SettingsPort is the modules: section of the node's file as it is now
// (settings.ModulesFile).
type SettingsPort interface {
	Modules() ([]settings.Module, error)
	SetOff(name string, off bool, why string) error
	SetModule(m settings.Module) error
	RemoveModule(name string) error
}

// Deps are the host's collaborators; all are required.
type Deps struct {
	Store    Store
	Cells    *cells.Service
	Jobs     Jobs
	Inbox    Inbox
	Secrets  secrets.Source
	State    runner.State
	Settings SettingsPort
	Admins   alert.Admins
	Now      func() time.Time
	Log      *slog.Logger
}

// ErrNotStarted is a module that is active and on but could not start; the
// error names why in the node's words.
var ErrNotStarted = modstore.ErrNotStarted

// Host runs the owner's modules. Calls of a module are serialised by its
// runner; Reload waits for the calls in progress.
type Host struct {
	d  Deps
	in instruments

	mu      sync.RWMutex // guards running; a call holds it for reading
	running map[string]*loaded
	tools   atomic.Pointer[[]tool.Handler] // the running modules' tools, for the registry

	stateMu sync.Mutex // guards what follows
	stopped map[string]stop
	off     map[string]bool
	fails   map[string][]time.Time
	sent    map[string]string // escalation key → its hour
}

// New returns a host with no module running; Start loads them.
func New(d Deps) *Host {
	h := &Host{d: d, in: newInstruments(), running: map[string]*loaded{}, stopped: map[string]stop{},
		off: map[string]bool{}, fails: map[string][]time.Time{}, sent: map[string]string{}}
	h.tools.Store(&[]tool.Handler{})
	return h
}

// Name implements status.Source.
func (*Host) Name() string { return "modules-local" }

// Start loads every active local module that the settings do not turn off:
// its cells, its init, its cron jobs and its tools. A module that cannot
// start is logged and shown in /status; it does not stop the node. An error
// is the store's or the settings' own.
func (h *Host) Start(ctx context.Context) error {
	actives, mods, err := h.read(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range actives {
		if err := h.apply(ctx, a.Module, &a, settingOf(mods, a.Module)); err != nil {
			h.d.Log.Warn("modhost: a module did not start", "module", a.Module, "err", err)
		}
	}
	return h.clearOrphans(ctx, actives)
}

// Reload brings module in line with the store and the settings after an
// install, a rollback, a config change, off or on, or a removal: a module
// that is active and on is loaded again, from a fresh count of failures;
// one that is not is stopped and its jobs deleted, its cells kept.
// ErrNotStarted names why a module that should run does not.
func (h *Host) Reload(ctx context.Context, module string) error {
	actives, mods, err := h.read(ctx)
	if err != nil {
		return err
	}
	var a *modstore.Active
	if i := slices.IndexFunc(actives, func(x modstore.Active) bool { return x.Module == module }); i >= 0 {
		a = &actives[i]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.apply(ctx, module, a, settingOf(mods, module))
}

// read returns the active local modules and the settings' modules.
func (h *Host) read(ctx context.Context) ([]modstore.Active, []settings.Module, error) {
	actives, err := h.d.Store.ActiveLocal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("modhost: %w", err)
	}
	mods, err := h.d.Settings.Modules()
	if err != nil {
		return nil, nil, fmt.Errorf("modhost: %w", err)
	}
	return actives, mods, nil
}

// settingOf is the settings' line of module; a module with none runs with
// its defaults.
func settingOf(mods []settings.Module, module string) settings.Module {
	for _, m := range mods {
		if m.Name == module {
			return m
		}
	}
	return settings.Module{Name: module}
}

// apply loads module from a, or stops it when a is nil (not active) or the
// settings turn it off. It holds h.mu.
func (h *Host) apply(ctx context.Context, module string, a *modstore.Active, s settings.Module) error {
	if a == nil || s.Off {
		h.unload(ctx, module)
		h.setStopped(module, stop{}, a != nil)
		if err := h.d.Jobs.DeleteModule(ctx, module); err != nil {
			return fmt.Errorf("modhost: %w", err)
		}
		return nil
	}
	l, st, err := h.prepare(ctx, *a, s)
	if err != nil || st.reason != "" {
		h.unload(ctx, module)
		h.setStopped(module, st, false)
		return errors.Join(h.d.Jobs.DeleteModule(ctx, module), notStarted(module, st, err))
	}
	old := h.running[module]
	h.running[module] = l
	h.refreshTools()
	if old != nil {
		_ = old.mod.Close(ctx)
	}
	h.setStopped(module, stop{}, false)
	h.resetFailures(module)
	h.d.Log.Info("modhost: module running", "module", module, "version", l.version)
	return nil
}

// notStarted is the error of a module that did not start.
func notStarted(module string, st stop, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotStarted, module, err)
	}
	return fmt.Errorf("%w: %s: %s", ErrNotStarted, module, st.reason.words())
}

// unload stops module if it runs. It holds h.mu, so no call is in progress.
func (h *Host) unload(ctx context.Context, module string) {
	l, ok := h.running[module]
	if !ok {
		return
	}
	delete(h.running, module)
	h.refreshTools()
	_ = l.mod.Close(ctx)
	h.resetFailures(module)
}

// refreshTools publishes the running modules' tools. It holds h.mu.
func (h *Host) refreshTools() {
	var out []tool.Handler
	for _, l := range h.running {
		out = append(out, l.tools...)
	}
	slices.SortFunc(out, func(a, b tool.Handler) int { return strings.Compare(a.Spec().Name, b.Spec().Name) })
	h.tools.Store(&out)
}

// Handlers are the tools of every running module, one per tool trigger:
// the registry's dynamic source (tool.WithDynamic). It never blocks.
func (h *Host) Handlers() []tool.Handler {
	return *h.tools.Load()
}
