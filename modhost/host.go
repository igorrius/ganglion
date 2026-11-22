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
	// OpenEpisode opens the escalation episode of a kind for a build's sha;
	// false when one is open for it already. CloseEpisode takes one back,
	// ClearEpisodes closes all of a module's (modstore's escalations).
	OpenEpisode(ctx context.Context, module, kind, sha string, at time.Time) (bool, error)
	CloseEpisode(ctx context.Context, module, kind string) error
	ClearEpisodes(ctx context.Context, module string) error
	// CountNotify counts a module's message on day and says whether the
	// owner was told of its limit that day; MarkLimited notes they were
	// (modstore's notify).
	CountNotify(ctx context.Context, module string, day time.Time) (n int, limited bool, err error)
	MarkLimited(ctx context.Context, module string, day time.Time) error
	// NotifiedOn is how many messages a module counted on day, for its card.
	NotifiedOn(ctx context.Context, module string, day time.Time) (int, error)
	// RecordRun writes a run to the journal, Run reads one and Runs the
	// last n of a module, newest first (modstore's runs).
	RecordRun(ctx context.Context, r modstore.Run) error
	Run(ctx context.Context, module, id string) (modstore.Run, bool, error)
	Runs(ctx context.Context, module string, n int) ([]modstore.Run, error)
}

// Jobs keeps the modules' cron jobs (schedule.Store).
type Jobs interface {
	EnsureModule(ctx context.Context, j schedule.Job) error
	DeleteModule(ctx context.Context, module string) error
}

// Inbox takes the escalation, trigger and notify events; an IdemKey
// already stored is a no-op (the queue).
type Inbox interface {
	Put(ctx context.Context, e event.Event) error
}

// SettingsPort is the modules: section of the node's file as it is now
// (settings.ModulesFile).
type SettingsPort interface {
	Modules() ([]settings.Module, error)
	// Links are the links of the file; removing a module drops its own.
	Links() ([]settings.Link, error)
	SetOff(name string, off bool, why string) error
	SetModule(m settings.Module) error
	RemoveModule(name string) error
	SetNotifyTo(module string, names []string) error
}

// DestinationFunc resolves a destination's name to its transport and
// conversation; ok is false when there is none by that name.
type DestinationFunc func(name string) (transport, conv string, ok bool, err error)

// NoDestinations knows no destination.
func NoDestinations(string) (string, string, bool, error) { return "", "", false, nil }

// SecretsFor is the secret source of one module, by its full name: the
// module's own scope of the secret store, then the environment (ADR-0077).
type SecretsFor func(module string) secrets.Source

// OneSource is a SecretsFor that gives every module the same source: a
// node without a secret store.
func OneSource(src secrets.Source) SecretsFor {
	return func(string) secrets.Source { return src }
}

// ForgetSecrets drops what the node stores for a removed module, by its
// full name.
type ForgetSecrets func(ctx context.Context, module string) error

// NothingToForget is the ForgetSecrets of a node without a secret store.
func NothingToForget(context.Context, string) error { return nil }

// Deps are the host's collaborators; all are required.
type Deps struct {
	Store Store
	Cells *cells.Service
	Jobs  Jobs
	Inbox Inbox
	// Publisher stores a module's message in the NOTIFY stream; the core's
	// consumer hands it back to Deliver (R-BUS-7).
	Publisher Publisher
	// Secrets gives each module its own secret source; Forget drops a
	// removed module's stored secrets.
	Secrets SecretsFor
	Forget  ForgetSecrets
	// NodeSecrets are the names of the node's own secrets (the bot token,
	// the providers' and the search keys): no module reads one, even one
	// installed before the install refused them.
	NodeSecrets []string
	State       runner.State
	Settings    SettingsPort
	Admins      alert.Admins
	// Destinations resolves the names of a module's notify_to
	// (groups.Destinations.Resolve); NoDestinations knows none.
	Destinations DestinationFunc
	Now          func() time.Time
	// Location is the node's time zone: a module's daily notify limit
	// counts its days (R-RUN-16); nil is UTC.
	Location *time.Location
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
	unread  time.Time            // when Start could not read the store or the settings; zero when it could
	stops   map[string]bool      // the builds (sha) whose chain stopped past maxDepth, told once
	broken  map[string]time.Time // the links seen broken, by their words, since when (/status)

	episodeMu sync.Mutex                 // held across the store's calls on episodes, so the cache and the store change together
	episodes  map[string]map[Kind]string // module → kind → the sha of its open episode, a cache of the store's
	clean     map[string]bool            // modules known to have no open episode
}

// New returns a host with no module running; Start loads them.
func New(d Deps) *Host {
	if d.Location == nil {
		d.Location = time.UTC
	}
	h := &Host{d: d, in: newInstruments(), running: map[string]*loaded{}, stopped: map[string]stop{},
		off: map[string]bool{}, fails: map[string][]time.Time{}, stops: map[string]bool{}, episodes: map[string]map[Kind]string{}, clean: map[string]bool{}}
	h.tools.Store(&[]tool.Handler{})
	return h
}

// Name implements status.Source.
func (*Host) Name() string { return "modules-local" }

// Start loads every active local module that the settings do not turn off:
// its cells, its init, its cron jobs and its tools. Nothing here stops the
// node: a module that cannot start is logged and shown in /status, and so is
// a store or a settings file that cannot be read, when none starts; jobs of
// removed modules that cannot be cleared are logged and stay.
func (h *Host) Start(ctx context.Context) {
	actives, mods, err := h.read(ctx)
	if err != nil {
		h.d.Log.Error("modhost: the local modules are not loaded: the store or the settings cannot be read", "err", err)
		h.stateMu.Lock()
		h.unread = h.d.Now()
		h.stateMu.Unlock()
		return
	}
	defer h.checkLinks(ctx) // after the lock is let go
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range actives {
		if err := h.apply(ctx, a.Module, &a, settingOf(mods, a.Module)); err != nil {
			h.d.Log.Warn("modhost: a module did not start", "module", a.Module, "err", err)
		}
	}
	if err := h.clearOrphans(ctx, actives); err != nil {
		h.d.Log.Warn("modhost: the jobs of removed modules are not cleared", "err", err)
	}
}

// Reload brings module in line with the store and the settings after an
// install, a rollback, a config change, off or on, or a removal: a module
// that is active and on is loaded again, from a fresh count of failures and
// with its escalation episodes closed;
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
	s := settingOf(mods, module)
	if err := h.apply(ctx, module, a, s); err != nil {
		return err
	}
	if a != nil && !s.Off { // a version, a rollback or an "on" starts afresh (owner ruling of 2026-11-10)
		h.clearEpisodes(ctx, module)
	}
	return nil
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
