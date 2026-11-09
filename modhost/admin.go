package modhost

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
)

// This file is what /modules does with the owner's modules (R-RUN-10): the
// list, the card, config, off and on, rollback and remove. Each change ends
// in Reload, so it applies without a restart.

// fullName is the name a module has in the store and the settings: the
// owner may write it with or without the "local/" prefix.
func fullName(name string) string {
	if modstore.IsLocal(name) {
		return name
	}
	return modstore.LocalPrefix + name
}

// localVersions are the versions of the local modules that are not retired,
// by module in name order.
func (h *Host) localVersions(ctx context.Context) (map[string][]modstore.Version, []string, error) {
	vs, err := h.d.Store.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("modhost: %w", err)
	}
	by := map[string][]modstore.Version{}
	var names []string
	for _, v := range vs {
		if v.Source != modstore.SourceLocal {
			continue
		}
		if _, seen := by[v.Module]; !seen {
			names = append(names, v.Module)
		}
		by[v.Module] = append(by[v.Module], v)
	}
	return by, names, nil
}

// activeVersion is the number of the active version in vs, "" when none.
func activeVersion(vs []modstore.Version) string {
	if i := slices.IndexFunc(vs, func(v modstore.Version) bool { return v.State == modstore.StateActive }); i >= 0 {
		return vs[i].Version
	}
	return ""
}

// Local lists the local modules with how each stands.
func (h *Host) Local(ctx context.Context) ([]modstore.LocalModule, error) {
	by, names, err := h.localVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modstore.LocalModule, 0, len(names))
	for _, n := range names {
		out = append(out, modstore.LocalModule{Name: modstore.ShortName(n), State: h.runState(n), Version: activeVersion(by[n])})
	}
	return out, nil
}

// runState is how module stands: a crash loop is told even while the module
// is off, since it is the node that turned it off.
func (h *Host) runState(module string) modstore.RunState {
	h.mu.RLock()
	_, running := h.running[module]
	h.mu.RUnlock()
	if running {
		return modstore.RunRunning
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	switch st, stopped := h.stopped[module]; {
	case stopped && st.reason == ReasonCrashloop:
		return modstore.RunCrashloop
	case h.off[module]:
		return modstore.RunOff
	}
	return modstore.RunStopped
}

// known is the module's non-retired versions, or ErrUnknownModule.
func (h *Host) known(ctx context.Context, module string) ([]modstore.Version, error) {
	by, _, err := h.localVersions(ctx)
	if err != nil {
		return nil, err
	}
	vs, ok := by[module]
	if !ok {
		return nil, fmt.Errorf("%w %q", modstore.ErrUnknownModule, module)
	}
	return vs, nil
}

// Card is how a module stands, every version it has had and the config keys
// it declares with their values now.
func (h *Host) Card(ctx context.Context, name string) (modstore.Card, error) {
	module := fullName(name)
	vs, err := h.known(ctx, module)
	if err != nil {
		return modstore.Card{}, err
	}
	journal, err := h.d.Store.Journal(ctx, module)
	if err != nil {
		return modstore.Card{}, fmt.Errorf("modhost: %w", err)
	}
	c := modstore.Card{Name: modstore.ShortName(module), State: h.runState(module), Version: activeVersion(vs), Journal: journal}
	m, ok, err := h.manifestOf(ctx, module)
	if err != nil || !ok {
		return c, err
	}
	mods, err := h.d.Settings.Modules()
	if err != nil {
		return modstore.Card{}, fmt.Errorf("modhost: %w", err)
	}
	have := settingOf(mods, module).Config
	for _, key := range slices.Sorted(maps.Keys(m.Config)) {
		k := m.Config[key]
		line := modstore.ConfigLine{Key: key, Type: k.Type, Required: k.Required, Default: k.Default}
		if v, set := have[key]; set {
			line.Value, line.Set = fmt.Sprint(v), true
		}
		c.Config = append(c.Config, line)
	}
	return c, nil
}

// manifestOf is the active version's manifest; false when none is active.
func (h *Host) manifestOf(ctx context.Context, module string) (runner.Manifest, bool, error) {
	as, err := h.d.Store.ActiveLocal(ctx)
	if err != nil {
		return runner.Manifest{}, false, fmt.Errorf("modhost: %w", err)
	}
	i := slices.IndexFunc(as, func(a modstore.Active) bool { return a.Module == module })
	if i < 0 {
		return runner.Manifest{}, false, nil
	}
	m, err := runner.ParseManifest(as[i].Manifest)
	if err != nil {
		return runner.Manifest{}, false, fmt.Errorf("modhost: %w", err)
	}
	return m, true, nil
}

// Configure sets one config key of a module: the key must be one the
// manifest declares and the value of its type (string, number or boolean);
// the settings line is written and the module reloaded. A refusal is a
// *modstore.ConfigError and never holds the value; a module that does not
// start after the change is modstore.ErrNotStarted, with the key set.
func (h *Host) Configure(ctx context.Context, name, key, value string) error {
	module := fullName(name)
	if _, err := h.known(ctx, module); err != nil {
		return err
	}
	m, ok, err := h.manifestOf(ctx, module)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w %q", modstore.ErrUnknownModule, module)
	}
	typed, err := typedValue(m, key, value)
	if err != nil {
		return err
	}
	mods, err := h.d.Settings.Modules()
	if err != nil {
		return fmt.Errorf("modhost: %w", err)
	}
	cur := settingOf(mods, module)
	cfg := maps.Clone(cur.Config)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg[key] = typed
	if err := h.d.Settings.SetModule(settings.Module{Name: module, Config: cfg, Off: cur.Off}); err != nil {
		return fmt.Errorf("modhost: %w", err)
	}
	return h.Reload(ctx, module)
}

// typedValue is value as the Go value of the key's declared type.
func typedValue(m runner.Manifest, key, value string) (any, error) {
	k, ok := m.Config[key]
	if !ok {
		return nil, &modstore.ConfigError{Err: modstore.ErrUndeclaredKey, Key: key, Declared: slices.Sorted(maps.Keys(m.Config))}
	}
	wrong := &modstore.ConfigError{Err: modstore.ErrWrongType, Key: key, Type: k.Type}
	switch k.Type {
	case "number":
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, wrong
		}
		return f, nil
	case "boolean":
		switch value {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, wrong
	}
	return value, nil
}

// SetOff turns a module off or on: off: true on its settings line, or none;
// either way it is reloaded, which stops it or starts it. Turning a crash-
// looping module on gives it a fresh count. modstore.ErrNotStarted says an
// "on" module did not start.
func (h *Host) SetOff(ctx context.Context, name string, off bool) error {
	module := fullName(name)
	if _, err := h.known(ctx, module); err != nil {
		return err
	}
	why := ""
	if off {
		why = "owner"
	}
	if err := h.d.Settings.SetOff(module, off, why); err != nil {
		return fmt.Errorf("modhost: %w", err)
	}
	return h.Reload(ctx, module)
}

// Rollback returns a module to its previous version and reloads it.
func (h *Host) Rollback(ctx context.Context, name string) (modstore.Outcome, error) {
	module := fullName(name)
	o, err := h.d.Store.Rollback(ctx, module)
	if err != nil || o.Result != modstore.ResultRolledBack {
		return o, err
	}
	return o, h.Reload(ctx, module)
}

// Remove takes a module out of service: its versions retire, it stops and
// its jobs go, its settings line goes, and its cells lose their owner. The
// cells and their history stay (ADR-0062), and so does the journal.
func (h *Host) Remove(ctx context.Context, name string) error {
	module := fullName(name)
	if err := h.d.Store.RemoveLocal(ctx, module); err != nil {
		return err
	}
	err := h.Reload(ctx, module)
	_, derr := h.d.Cells.Disown(ctx, module)
	return errors.Join(err, h.d.Settings.RemoveModule(module), derr)
}
