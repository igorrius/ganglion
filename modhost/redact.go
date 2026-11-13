package modhost

import (
	"log/slog"
	"slices"
	"sync"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/telemetry"
)

// keptPerSecret bounds the values remembered for one secret name: the
// current one and those it replaced while the module ran (a rotated
// NAME_FILE); the oldest goes first.
const keptPerSecret = 8

// given is what one module instance was handed of its granted secrets:
// the values read at load and every value secret_get gave it since, by
// name. The node's redactor knows only the node's own secrets, and a module
// may echo one it was granted into its log or its error, so these are
// hidden from every line its logger writes (no secret value in a log).
// Like every exact-value redaction, a value shorter than six characters is
// not hidden (redact.Values).
type given struct {
	mu     sync.Mutex
	byName map[string][]string
	hide   func(string) string
}

// newGiven is the set of the granted secrets of module, whose manifest is
// m, as the node holds them now.
func (h *Host) newGiven(module string, m runner.Manifest) *given {
	src := h.d.Secrets(module)
	g := &given{byName: map[string][]string{}, hide: func(s string) string { return s }}
	for _, name := range m.Capabilities.SecretGet {
		if modstore.NodeSecret(h.d.NodeSecrets, name) {
			continue // never given to a module, and hidden by the node's own redactor
		}
		if v, err := src.Secret(name); err == nil {
			g.add(name, v)
		}
	}
	return g
}

// add remembers that the module holds value for name; the hiding is
// rebuilt only when the set grows.
func (g *given) add(name, value string) {
	if value == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	vs := g.byName[name]
	if slices.Contains(vs, value) {
		return
	}
	vs = append(vs, value)
	if len(vs) > keptPerSecret {
		vs = vs[len(vs)-keptPerSecret:]
	}
	g.byName[name] = vs
	var all []string
	for _, v := range g.byName {
		all = append(all, v...)
	}
	g.hide = redact.Values(all...)
}

// Hide replaces every value the module was given with "[redacted]".
func (g *given) Hide(s string) string {
	g.mu.Lock()
	hide := g.hide
	g.mu.Unlock()
	return hide(s)
}

// logged is a module's text for a log line: hidden first, then capped, so a
// value that straddles the cut is never half logged.
func (g *given) logged(s string) string {
	return text.Truncate(g.Hide(s), maxLogged)
}

// moduleLog is the logger for everything a module's run writes to the
// node's log: the runner's lines (the module's own log, a failed init or
// health) and the host's (a failed run, its text), each line through g.
func (h *Host) moduleLog(g *given) *slog.Logger {
	return slog.New(telemetry.RedactHandler(h.d.Log.Handler(), g.Hide))
}
