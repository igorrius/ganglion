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
		if m.Tier != router.TierCheap && m.Tier != router.TierSmart {
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

// Route implements router.Router: the models that fit the task, the
// task's tier first, cooling models last, then by price, latency and
// catalogue order.
func (r *Router) Route(ctx context.Context, t router.TaskReq) ([]router.Candidate, error) {
	picked := r.pick(t, r.overBudget(ctx))
	if len(picked) == 0 {
		return nil, router.ErrNoCandidate
	}
	r.rank(picked, taskTier(t))
	out := make([]router.Candidate, len(picked))
	for i, p := range picked {
		out[i] = router.Candidate{Provider: p.m.Provider, Model: p.m.Name, PriceInMTok: p.m.Caps.PriceInMTok, PriceOutMTok: p.m.Caps.PriceOutMTok}
	}
	return out, nil
}

// ranked is a model with its catalogue position, the last tie-breaker.
type ranked struct {
	m     router.Model
	order int
}

// taskTier is the tier a task prefers: its own, or smart for a message
// and cheap for anything else.
func taskTier(t router.TaskReq) router.Tier {
	switch {
	case t.Tier != "":
		return t.Tier
	case t.Kind == router.TaskMessage:
		return router.TierSmart
	default:
		return router.TierCheap
	}
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

// pick keeps the models that fit the task; over budget, paid cloud smart
// models drop out unless the task asks for smart.
func (r *Router) pick(t router.TaskReq, overBudget bool) []ranked {
	var picked []ranked
	for i, m := range r.models {
		if !fits(m, t) {
			continue
		}
		if overBudget && t.Tier != router.TierSmart && m.Tier == router.TierSmart && price(m) > 0 && !m.Caps.Local {
			continue
		}
		picked = append(picked, ranked{m, i})
	}
	return picked
}

// rank orders picked: the preferred tier before the other, models not
// cooling before cooling ones, then cheaper, faster and earlier.
func (r *Router) rank(picked []ranked, tier router.Tier) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	group := func(x ranked) int {
		g := 0
		if x.m.Tier != tier {
			g = 1
		}
		if r.cooling[key(x.m.Provider, x.m.Name)].After(now) {
			g += 2
		}
		return g
	}
	slices.SortStableFunc(picked, func(a, b ranked) int {
		return cmp.Or(
			cmp.Compare(group(a), group(b)),
			cmp.Compare(price(a.m), price(b.m)),
			cmp.Compare(a.m.Caps.Latency, b.m.Caps.Latency),
			cmp.Compare(a.order, b.order),
		)
	})
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
