// Package rules is the P1 router: each purpose's ordered list of models,
// hard filters, a daily budget and a cooldown for rate-limited models
// (docs/spec.md, 7.3, R-RTR-1, R-RTR-2 and R-RTR-6).
package rules

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
)

// LongContext is the context size a "long" task needs.
const LongContext = 100_000

// DefaultCooldown is how long a rate-limited model goes to the back of the
// list when the provider names no shorter Retry-After.
const DefaultCooldown = 60 * time.Second

// AuthCooldown is how long every model of a provider goes to the back after
// an auth error: a wrong key or a lost login is not fixed in a minute.
const AuthCooldown = 10 * time.Minute

// Budget reports money spent since a moment (the turns trace).
type Budget interface {
	SpentSince(ctx context.Context, t time.Time) (usd float64, err error)
}

// Router is a router.Router over a fixed catalogue and its purposes.
type Router struct {
	models   []router.Model
	byID     map[string]router.Model
	purposes map[router.Purpose][]router.Choice
	now      func() time.Time
	budget   Budget
	daily    float64
	loc      *time.Location
	cooldown time.Duration

	mu      sync.Mutex
	cooling map[string]time.Time // provider/model → until
}

var _ router.Router = (*Router)(nil)

// Option configures a Router.
type Option func(*Router)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(r *Router) { r.now = now } }

// WithBudget sets the daily budget: over b's daily budget, chat keeps free
// and local models only. The day starts at midnight in loc; the other
// purposes are housekeeping and are not cut.
func WithBudget(b Budget, dailyUSD float64, loc *time.Location) Option {
	return func(r *Router) { r.budget, r.daily, r.loc = b, dailyUSD, loc }
}

// WithCooldown sets the minimum cooldown after a rate limit.
func WithCooldown(d time.Duration) Option { return func(r *Router) { r.cooldown = d } }

// New returns a router over models with each purpose's ordered list.
// Every choice must name a model; settings checks this first, and New
// checks it again so a router is never built over a dangling id.
func New(models []router.Model, purposes map[router.Purpose][]router.Choice, opts ...Option) (*Router, error) {
	r := &Router{byID: map[string]router.Model{}, purposes: purposes, now: time.Now, loc: time.UTC,
		cooldown: DefaultCooldown, cooling: map[string]time.Time{}, models: slices.Clone(models)}
	for _, o := range opts {
		o(r)
	}
	for _, m := range models {
		r.byID[m.ID] = m
	}
	for p, list := range purposes {
		for _, c := range list {
			if _, ok := r.byID[c.Model]; !ok {
				return nil, fmt.Errorf("rules: purpose %s names no model %q", p, c.Model)
			}
		}
	}
	return r, nil
}

func key(provider, model string) string { return provider + "/" + model }

func fits(m router.Model, t router.TaskReq) bool {
	switch {
	case t.Tools && !m.Caps.Tools,
		t.Vision && !m.Caps.Vision,
		t.Context == "long" && m.Caps.ContextTokens < LongContext,
		t.Privacy == "local-only" && !m.Caps.Local:
		return false
	}
	return true
}

func price(m router.Model) float64 { return m.Caps.PriceInMTok + m.Caps.PriceOutMTok }

// Route implements router.Router: the purpose's list in order, without the
// models that do not fit the task; over budget, chat keeps free and local
// models only; cooling models move to the end.
func (r *Router) Route(ctx context.Context, t router.TaskReq) ([]router.Candidate, error) {
	over := t.Purpose == router.PurposeChat && r.overBudget(ctx)
	var out, cooling []router.Candidate
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.purposes[t.Purpose] {
		m := r.byID[c.Model]
		if !fits(m, t) || (over && price(m) > 0 && !m.Caps.Local) {
			continue
		}
		cand := router.Candidate{Provider: m.Provider, Model: m.Name, Effort: c.Effort,
			PriceInMTok: m.Caps.PriceInMTok, PriceOutMTok: m.Caps.PriceOutMTok}
		if r.cooling[key(m.Provider, m.Name)].After(now) {
			cooling = append(cooling, cand)
			continue
		}
		out = append(out, cand)
	}
	out = append(out, cooling...)
	if len(out) == 0 {
		return nil, router.ErrNoCandidate
	}
	return out, nil
}

// overBudget reports whether today's spend has reached the daily budget.
// On an error the budget does not block: fail open.
func (r *Router) overBudget(ctx context.Context) bool {
	if r.budget == nil || r.daily <= 0 {
		return false
	}
	spent, err := r.budget.SpentSince(ctx, clock.DayStart(r.now(), r.loc))
	return err == nil && spent >= r.daily
}

// Report implements router.Router: a rate limit sends the model to the
// back of the list until max(Retry-After, cooldown) has passed; an auth
// error sends every model of the provider back for AuthCooldown.
func (r *Router) Report(c router.Candidate, err error) {
	e, ok := errors.AsType[*provider.Error](err)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cool := func(k string, until time.Time) {
		if until.After(r.cooling[k]) {
			r.cooling[k] = until
		}
	}
	switch e.Kind {
	case provider.KindRateLimit:
		cool(key(c.Provider, c.Model), r.now().Add(max(e.RetryAfter, r.cooldown)))
	case provider.KindAuth:
		for _, m := range r.models {
			if m.Provider == c.Provider {
				cool(key(m.Provider, m.Name), r.now().Add(AuthCooldown))
			}
		}
	default:
		// Timeouts, outages and bad requests are not the model's state:
		// the next turn may try it again.
	}
}
