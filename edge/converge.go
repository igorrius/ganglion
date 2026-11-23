package edge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/settings"
)

// This file is the edge's side of module delivery (R-EDGE-6, ADR-0117): a
// Converger watches the edge's desired keys and brings its modules in line
// with them. It runs nothing it has not checked: the key must be the
// edge's own, the value well formed, the files must match the digest in
// the value, and the build must load and pass its health trigger before it
// is installed and its init runs. A version that fails any of these is
// refused and the one before it keeps running; either way the edge writes
// what it runs to actual.<edge>.<module>.

// MaxModuleFile is the largest .wasm or manifest an edge fetches: the
// builder's own limit on a .wasm.
const MaxModuleFile = 16 << 20

// Update is one of the edge's desired keys as the watch hands it over: the
// edge and the module from the key.
type Update struct {
	Edge, Module string
	Value        []byte
	Deleted      bool
}

// Host is the edge's module host (modhost.Host).
type Host interface {
	Check(ctx context.Context, b modstore.Build, config map[string]any) error
	Reload(ctx context.Context, module string) error
	Rollback(ctx context.Context, name string) (modstore.Outcome, error)
}

// Store is the edge's module store (modstore.Store).
type Store interface {
	List(ctx context.Context) ([]modstore.Version, error)
	InstallDelivered(ctx context.Context, b modstore.Build, sha string) (modstore.Outcome, error)
	RemoveLocal(ctx context.Context, module string) error
}

// Lines reads and writes the modules' lines in the edge's file: their
// config from the desired value (settings.ModulesFile).
type Lines interface {
	Modules() ([]settings.Module, error)
	SetModule(m settings.Module) error
}

// Converger is the edge's component that runs what the core says it should.
type Converger struct {
	// Edge is the edge's name.
	Edge string
	// Watch calls fn with each desired key of edge, then every change,
	// until stop (natsbus.Bus.WatchDesired).
	Watch func(ctx context.Context, edge string, fn func(Update)) (stop func() error, err error)
	// Fetch reads a build's files from the core, each at most limit bytes
	// (natsbus.Bus.GetModule).
	Fetch func(ctx context.Context, module, digest string, limit int64) (wasm, manifest []byte, err error)
	// Report writes what the edge runs of module (natsbus.Bus.PutActual).
	Report func(ctx context.Context, edge, module string, value []byte) error
	// Linked tells whether the link to the core is up.
	Linked func(ctx context.Context) bool
	Host   Host
	Store  Store
	Lines  Lines
	// Poll is how often the link is looked at, 0 a second; Resync how
	// often the watch starts afresh and every key is reported again, 0
	// five minutes.
	Poll, Resync time.Duration
	Now          func() time.Time
	Log          *slog.Logger

	mu      sync.Mutex
	pending map[string]Update // the latest update of each module, not yet applied
	kick    chan struct{}

	refused  map[string]Actual         // module → the refusal of the digest it was told, kept until told another
	reported map[string]Actual         // module → what was last reported, At aside
	configs  map[string]map[string]any // module → the config it runs with
}

// Name implements node.Component.
func (c *Converger) Name() string { return "edge-delivery" }

// retryWatch is how long the converger waits before it tries again a watch
// that could not start while the link is up.
const retryWatch = 10 * time.Second

// Run watches the edge's desired keys and applies them until ctx ends.
// The watch starts when the link is up, starts again when it comes back
// and every Resync, so a change missed while the link was down is read.
func (c *Converger) Run(ctx context.Context) error {
	c.init()
	poll, resync := c.Poll, c.Resync
	if poll <= 0 {
		poll = time.Second
	}
	if resync <= 0 {
		resync = 5 * time.Minute
	}
	look := time.NewTicker(poll)
	defer look.Stop()
	again := time.NewTicker(resync)
	defer again.Stop()
	var (
		stop    func() error
		linked  bool
		tried   time.Time
		restart = func() {
			if stop != nil {
				_ = stop()
				stop = nil
			}
			tried = c.Now()
			s, err := c.Watch(ctx, c.Edge, c.take)
			if err != nil {
				c.Log.Warn("edge: the desired modules cannot be watched; trying again", "err", err)
				return
			}
			stop = s
			c.mu.Lock()
			c.reported = map[string]Actual{} // every key is reported again
			c.mu.Unlock()
		}
	)
	defer func() {
		if stop != nil {
			_ = stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.kick:
			c.drain(ctx)
		case <-look.C:
			l := c.Linked(ctx)
			if l && (!linked || (stop == nil && c.Now().Sub(tried) >= retryWatch)) {
				restart()
			}
			linked = l
		case <-again.C:
			if linked {
				restart()
			}
		}
	}
}

func (c *Converger) init() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kick == nil {
		c.kick = make(chan struct{}, 1)
		c.pending, c.refused, c.reported, c.configs = map[string]Update{}, map[string]Actual{}, map[string]Actual{}, map[string]map[string]any{}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
}

// take keeps u as its module's latest update and wakes Run; it never
// blocks the watch.
func (c *Converger) take(u Update) {
	c.mu.Lock()
	c.pending[u.Module] = u
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// drain applies the pending updates in module order.
func (c *Converger) drain(ctx context.Context) {
	c.mu.Lock()
	ups := c.pending
	c.pending = map[string]Update{}
	c.mu.Unlock()
	for _, m := range slices.Sorted(maps.Keys(ups)) {
		c.Apply(ctx, ups[m])
	}
}

// Apply brings one module in line with its desired key and reports what
// runs. It is Run's step, exported for the tests of the edge's role.
func (c *Converger) Apply(ctx context.Context, u Update) {
	c.init()
	if u.Edge != c.Edge {
		// The watch reads only this edge's keys, and the edge's user may read
		// no other; a key of another edge is never acted on.
		c.Log.Warn("edge: a desired key of another edge is refused", "edge", c.Edge, "key_edge", u.Edge, "module", u.Module)
		return
	}
	if modstore.CheckShortName(u.Module) != "" {
		c.Log.Warn("edge: a desired key that cannot name a module is refused", "module", u.Module)
		return
	}
	module := modstore.LocalPrefix + u.Module
	if u.Deleted {
		c.remove(ctx, u.Module, module)
		return
	}
	d, err := decodeDesired(u.Module, u.Value)
	if err != nil {
		c.refuse(ctx, u.Module, "", "the desired value is not one this edge reads")
		return
	}
	cur := c.active(ctx, module)
	switch {
	case cur.SHA256 == d.SHA256:
		c.keepConfig(ctx, module, d)
		c.report(ctx, u.Module, d.SHA256, modstore.EdgeRunning, "")
		return
	case c.wasRefused(ctx, u.Module, d.SHA256):
		return
	}
	wasm, manifest, err := c.Fetch(ctx, u.Module, d.SHA256, MaxModuleFile)
	if err != nil {
		// The link or the core may be at fault: not kept, tried again at the
		// next start of the watch.
		c.Log.Warn("edge: a delivered module's files cannot be fetched", "module", module, "err", err)
		c.report(ctx, u.Module, d.SHA256, modstore.EdgeRefused, "its files cannot be fetched from the core")
		return
	}
	if modstore.Digest(wasm, manifest) != d.SHA256 {
		c.Log.Warn("edge: a delivered module's files do not match its digest; nothing of it runs", "module", module)
		c.refuse(ctx, u.Module, d.SHA256, "its files do not match the digest the core wrote")
		return
	}
	b := modstore.Build{Module: module, Wasm: wasm, Manifest: manifest, By: "core", Note: "delivered by the core"}
	if err := c.Host.Check(ctx, b, d.Config); err != nil {
		c.refuse(ctx, u.Module, d.SHA256, err.Error())
		return
	}
	c.install(ctx, u.Module, b, d, cur)
}

// install installs a checked build, runs it, and rolls back to cur when its
// init fails.
func (c *Converger) install(ctx context.Context, short string, b modstore.Build, d Desired, cur modstore.Version) {
	if err := c.Lines.SetModule(settings.Module{Name: b.Module, Config: d.Config}); err != nil {
		c.Log.Warn("edge: a delivered module's config is not written; it runs with its defaults", "module", b.Module, "err", err)
	} else {
		c.setConfig(short, d.Config)
	}
	o, err := c.Store.InstallDelivered(ctx, b, d.SHA256)
	switch {
	case err != nil:
		c.Log.Warn("edge: a delivered module is not installed", "module", b.Module, "err", err)
		c.report(ctx, short, d.SHA256, modstore.EdgeRefused, "it could not be stored on the edge")
		return
	case o.Result == modstore.ResultRefused:
		c.refuse(ctx, short, d.SHA256, o.Reason)
		return
	}
	if err := c.Host.Reload(ctx, b.Module); err != nil {
		c.Log.Warn("edge: a delivered module did not start; the version before it runs again", "module", b.Module, "err", err)
		if cur.SHA256 != "" && cur.SHA256 != d.SHA256 {
			if _, rerr := c.Host.Rollback(ctx, b.Module); rerr != nil {
				c.Log.Warn("edge: the rollback failed", "module", b.Module, "err", rerr)
			}
		} else {
			if rerr := c.Store.RemoveLocal(ctx, b.Module); rerr != nil && !errors.Is(rerr, modstore.ErrUnknownModule) {
				c.Log.Warn("edge: a module that did not start is not removed", "module", b.Module, "err", rerr)
			}
			_ = c.Host.Reload(ctx, b.Module)
		}
		c.refuse(ctx, short, d.SHA256, startWords(err))
		return
	}
	c.Log.Info("edge: delivered module running", "module", b.Module, "version", d.Version)
	c.report(ctx, short, d.SHA256, modstore.EdgeRunning, "")
}

// startWords is why a module did not start, in the node's words: the
// module host's error names the reason, never the module's text.
func startWords(err error) string {
	s := err.Error()
	if _, after, ok := strings.Cut(s, ": local/"); ok {
		if _, why, ok := strings.Cut(after, ": "); ok {
			return why
		}
	}
	return "it did not start"
}

// keepConfig writes the config of a module that runs the told digest when
// it differs from what the module runs with, and reloads it.
func (c *Converger) keepConfig(ctx context.Context, module string, d Desired) {
	c.mu.Lock()
	had, ok := c.configs[d.Module]
	c.mu.Unlock()
	if !ok {
		had, ok = c.fileConfig(module)
	}
	if ok && configEqual(had, d.Config) {
		c.setConfig(d.Module, d.Config)
		return
	}
	if err := c.Lines.SetModule(settings.Module{Name: module, Config: d.Config}); err != nil {
		c.Log.Warn("edge: a delivered module's config is not written", "module", module, "err", err)
		return
	}
	c.setConfig(d.Module, d.Config)
	if err := c.Host.Reload(ctx, module); err != nil {
		c.Log.Warn("edge: a module did not start again with its config", "module", module, "err", err)
	}
}

// fileConfig is the config of module's line in the edge's file; false when
// the file cannot be read.
func (c *Converger) fileConfig(module string) (map[string]any, bool) {
	mods, err := c.Lines.Modules()
	if err != nil {
		return nil, false
	}
	for _, m := range mods {
		if m.Name == module {
			return m.Config, true
		}
	}
	return nil, true
}

func (c *Converger) setConfig(short string, cfg map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configs[short] = cfg
}

// configEqual compares two configs as they are written: an empty one and
// none are the same.
func configEqual(a, b map[string]any) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// remove takes a module whose desired key is gone out of service.
func (c *Converger) remove(ctx context.Context, short, module string) {
	if c.active(ctx, module).SHA256 == "" {
		return
	}
	if err := c.Store.RemoveLocal(ctx, module); err != nil && !errors.Is(err, modstore.ErrUnknownModule) {
		c.Log.Warn("edge: a module the core withdrew is not removed", "module", module, "err", err)
		return
	}
	_ = c.Host.Reload(ctx, module)
	c.mu.Lock()
	delete(c.refused, short)
	c.mu.Unlock()
	c.Log.Info("edge: module withdrawn by the core", "module", module)
	c.report(ctx, short, "", modstore.EdgeRemoved, "")
}

// refuse reports the told digest refused and keeps the refusal, so the
// same digest is not tried again until the core tells another or the edge
// starts again; "" digest is not kept.
func (c *Converger) refuse(ctx context.Context, short, digest, reason string) {
	c.Log.Warn("edge: a delivered module is refused; what ran before keeps running", "module", short, "reason", reason)
	if digest != "" {
		c.mu.Lock()
		c.refused[short] = Actual{Desired: digest, Reason: reason}
		c.mu.Unlock()
	}
	c.report(ctx, short, digest, modstore.EdgeRefused, reason)
}

// wasRefused reports a digest already refused, reporting it again when
// the watch started afresh.
func (c *Converger) wasRefused(ctx context.Context, short, digest string) bool {
	c.mu.Lock()
	r, ok := c.refused[short]
	c.mu.Unlock()
	if !ok || r.Desired != digest {
		return false
	}
	c.report(ctx, short, digest, modstore.EdgeRefused, r.Reason)
	return true
}

// active is the module's active version on the edge; zero when none.
func (c *Converger) active(ctx context.Context, module string) modstore.Version {
	vs, err := c.Store.List(ctx)
	if err != nil {
		c.Log.Warn("edge: the module store cannot be read", "err", err)
		return modstore.Version{}
	}
	for _, v := range vs {
		if v.Module == module && v.State == modstore.StateActive {
			return v
		}
	}
	return modstore.Version{}
}

// report writes what runs of the module, unless it is what was last
// reported.
func (c *Converger) report(ctx context.Context, short, desired, result, reason string) {
	run := c.active(ctx, modstore.LocalPrefix+short)
	a := Actual{V: DeliveryVersion, Module: short, Version: run.Version, SHA256: run.SHA256, Desired: desired, Result: result, Reason: reason}
	c.mu.Lock()
	last, ok := c.reported[short]
	c.mu.Unlock()
	if ok && last == a {
		return
	}
	stamped := a
	stamped.At = c.Now().UTC()
	data, err := json.Marshal(stamped)
	if err == nil {
		err = c.Report(ctx, c.Edge, short, data)
	}
	if err != nil {
		c.Log.Warn("edge: what runs is not reported to the core", "module", short, "err", err)
		return
	}
	c.mu.Lock()
	c.reported[short] = a
	c.mu.Unlock()
}
