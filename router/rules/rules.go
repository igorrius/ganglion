// Package rules is the P1 router: hard filters, tier preference, a daily
// budget and a cooldown for rate-limited models, then price and latency
// (docs/spec.md, 7.3, R-RTR-1 and R-RTR-2).
package rules

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
)

// LongContext is the context size a "long" task needs.
const LongContext = 100_000

// DefaultCooldown is how long a rate-limited model goes to the back of the
// list when the provider names no shorter Retry-After.
const DefaultCooldown = 60 * time.Second

// Budget reports money spent since a moment (the turns trace).
type Budget interface {
	SpentSince(ctx context.Context, t time.Time) (usd float64, err error)
}

// Router is a router.Router over a fixed catalogue.
type Router struct {
	models   []router.Model
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

// WithBudget drops paid smart models once b reports dailyUSD spent since
// the start of the day in loc, unless the task asks for smart.
func WithBudget(b Budget, dailyUSD float64, loc *time.Location) Option {
	return func(r *Router) { r.budget, r.daily, r.loc = b, dailyUSD, loc }
}

// WithCooldown sets the minimum cooldown after a rate limit.
func WithCooldown(d time.Duration) Option { return func(r *Router) { r.cooldown = d } }

// New returns a router over models, in catalogue order.
func New(models []router.Model, opts ...Option) (*Router, error) {
	r := &Router{models: slices.Clone(models), now: time.Now, loc: time.UTC, cooldown: DefaultCooldown, cooling: map[string]time.Time{}}
	for _, o := range opts {
		o(r)
	}
	for _, m := range r.models {
		if m.Tier != "cheap" && m.Tier != "smart" {
			return nil, errors.New("rules: model " + m.Provider + "/" + m.Name + ": tier must be cheap or smart")
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

// Route implements router.Router.
func (r *Router) Route(ctx context.Context, t router.TaskReq) ([]router.Candidate, error) {
	tier := t.Tier
	if tier == "" {
		tier = "cheap"
		if t.Kind == "message" {
			tier = "smart"
		}
	}
	overBudget := false
	if r.budget != nil && r.daily > 0 {
		now := r.now().In(r.loc)
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, r.loc)
		if spent, err := r.budget.SpentSince(ctx, day); err == nil && spent >= r.daily {
			overBudget = true // on an error the budget does not block: fail open
		}
	}

	type ranked struct {
		m     router.Model
		order int
	}
	var picked []ranked
	for i, m := range r.models {
		if !fits(m, t) {
			continue
		}
		if overBudget && t.Tier != "smart" && m.Tier == "smart" && price(m) > 0 && !m.Caps.Local {
			continue
		}
		picked = append(picked, ranked{m, i})
	}
	if len(picked) == 0 {
		return nil, router.ErrNoCandidate
	}

	now := r.now()
	r.mu.Lock()
	cool := func(m router.Model) bool { return r.cooling[key(m.Provider, m.Name)].After(now) }
	slices.SortStableFunc(picked, func(a, b ranked) int {
		group := func(x ranked) int {
			g := 0
			if x.m.Tier != tier {
				g = 1
			}
			if cool(x.m) {
				g += 2
			}
			return g
		}
		return cmp.Or(
			cmp.Compare(group(a), group(b)),
			cmp.Compare(price(a.m), price(b.m)),
			cmp.Compare(a.m.Caps.Latency, b.m.Caps.Latency),
			cmp.Compare(a.order, b.order),
		)
	})
	r.mu.Unlock()

	out := make([]router.Candidate, len(picked))
	for i, p := range picked {
		out[i] = router.Candidate{Provider: p.m.Provider, Model: p.m.Name, PriceInMTok: p.m.Caps.PriceInMTok, PriceOutMTok: p.m.Caps.PriceOutMTok}
	}
	return out, nil
}

// Report implements router.Router: a rate limit sends the model to the
// back of the list until max(Retry-After, cooldown) has passed.
func (r *Router) Report(c router.Candidate, err error) {
	e, ok := errors.AsType[*provider.Error](err)
	if !ok || e.Kind != provider.KindRateLimit {
		return
	}
	until := r.now().Add(max(e.RetryAfter, r.cooldown))
	r.mu.Lock()
	defer r.mu.Unlock()
	if until.After(r.cooling[key(c.Provider, c.Model)]) {
		r.cooling[key(c.Provider, c.Model)] = until
	}
}
