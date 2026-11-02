package rules

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type budget struct {
	usd   float64
	since time.Time
}

func (b *budget) SpentSince(_ context.Context, t time.Time) (float64, error) {
	b.since = t
	return b.usd, nil
}

func m(provider, name, tier string, caps router.ModelCaps) router.Model {
	return router.Model{Provider: provider, Name: name, Tier: tier, Caps: caps}
}

var catalogue = []router.Model{
	m("or", "smart-pricey", "smart", router.ModelCaps{Tools: true, ContextTokens: 200000, PriceInMTok: 3, PriceOutMTok: 15}),
	m("or", "cheap-a", "cheap", router.ModelCaps{Tools: true, ContextTokens: 64000, PriceInMTok: 0.3, PriceOutMTok: 1.2, Latency: 900 * time.Millisecond}),
	m("or", "cheap-b", "cheap", router.ModelCaps{Tools: true, ContextTokens: 128000, PriceInMTok: 0.3, PriceOutMTok: 1.2, Latency: 400 * time.Millisecond}),
	m("claude", "haiku", "cheap", router.ModelCaps{ContextTokens: 200000}),
	m("claude", "sonnet", "smart", router.ModelCaps{ContextTokens: 200000}),
	m("ollama", "local", "cheap", router.ModelCaps{Tools: true, Local: true, ContextTokens: 32000}),
	m("or", "vision", "smart", router.ModelCaps{Tools: true, Vision: true, ContextTokens: 128000, PriceInMTok: 1, PriceOutMTok: 4}),
}

func newRouter(t *testing.T, opts ...Option) (*Router, *clock) {
	t.Helper()
	c := &clock{now: t0}
	r, err := New(catalogue, append([]Option{WithClock(c.Now)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return r, c
}

func names(cs []router.Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Provider+"/"+c.Model)
	}
	return out
}

func route(t *testing.T, r *Router, task router.TaskReq) []string {
	t.Helper()
	cs, err := r.Route(context.Background(), task)
	if err != nil {
		t.Fatalf("Route(%+v): %v", task, err)
	}
	return names(cs)
}

func TestFiltersTools(t *testing.T) {
	r, _ := newRouter(t)
	for _, n := range route(t, r, router.TaskReq{Tools: true, Tier: "cheap"}) {
		if n == "claude/haiku" || n == "claude/sonnet" {
			t.Fatalf("model without tools offered for a tool task: %s", n)
		}
	}
}

func TestPrivacyLocalOnly(t *testing.T) {
	r, _ := newRouter(t)
	if got := route(t, r, router.TaskReq{Privacy: "local-only", Kind: "message"}); !slices.Equal(got, []string{"ollama/local"}) {
		t.Fatalf("local-only = %v", got)
	}
}

func TestLongContext(t *testing.T) {
	r, _ := newRouter(t)
	for _, n := range route(t, r, router.TaskReq{Context: "long", Tier: "cheap"}) {
		if n == "or/cheap-a" || n == "ollama/local" { // 64k and 32k; cheap-b has 128k
			t.Fatalf("short-context model %s offered for a long task", n)
		}
	}
}

func TestHumanMessageSmartFirst(t *testing.T) {
	r, _ := newRouter(t)
	got := route(t, r, router.TaskReq{Kind: "message"})
	want := []string{"claude/sonnet", "or/vision", "or/smart-pricey", "claude/haiku", "ollama/local", "or/cheap-b", "or/cheap-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("message route\n got %v\nwant %v", got, want)
	}
}

func TestCronCheapFirst(t *testing.T) {
	r, _ := newRouter(t)
	got := route(t, r, router.TaskReq{Kind: "cron"})
	if got[0] != "claude/haiku" || got[3] != "or/cheap-a" || got[4] != "claude/sonnet" {
		t.Fatalf("cron route %v", got)
	}
}

func TestExplicitTierWins(t *testing.T) {
	r, _ := newRouter(t)
	if got := route(t, r, router.TaskReq{Kind: "message", Tier: "cheap"}); got[0] != "claude/haiku" {
		t.Fatalf("explicit cheap = %v", got)
	}
}

func TestPriceOrderWithinTier(t *testing.T) {
	r, _ := newRouter(t)
	got := route(t, r, router.TaskReq{Kind: "cron", Tools: true})
	// cheap-a and cheap-b cost the same; cheap-b is faster.
	if !slices.Equal(got[:3], []string{"ollama/local", "or/cheap-b", "or/cheap-a"}) {
		t.Fatalf("price order %v", got)
	}
}

func TestCandidateCarriesPrices(t *testing.T) {
	r, _ := newRouter(t)
	cs, _ := r.Route(context.Background(), router.TaskReq{Kind: "message", Vision: true})
	if len(cs) != 1 || cs[0].PriceInMTok != 1 || cs[0].PriceOutMTok != 4 {
		t.Fatalf("candidates %+v", cs)
	}
}

func TestCooldownAfterRateLimit(t *testing.T) {
	r, c := newRouter(t, WithCooldown(60*time.Second))
	r.Report(router.Candidate{Provider: "claude", Model: "sonnet"}, &provider.Error{Kind: provider.KindRateLimit, RetryAfter: 30 * time.Second})
	r.Report(router.Candidate{Provider: "or", Model: "vision"}, &provider.Error{Kind: provider.KindUnavailable})
	got := route(t, r, router.TaskReq{Kind: "message"})
	if got[0] != "or/vision" || got[len(got)-1] != "claude/sonnet" {
		t.Fatalf("after a rate limit %v", got)
	}
	c.now = t0.Add(61 * time.Second)
	if got := route(t, r, router.TaskReq{Kind: "message"}); got[0] != "claude/sonnet" {
		t.Fatalf("after the cooldown %v", got)
	}
	r.Report(router.Candidate{Provider: "claude", Model: "sonnet"}, &provider.Error{Kind: provider.KindRateLimit, RetryAfter: 5 * time.Minute})
	c.now = t0.Add(61*time.Second + 2*time.Minute)
	if got := route(t, r, router.TaskReq{Kind: "message"}); got[len(got)-1] != "claude/sonnet" {
		t.Fatalf("a longer Retry-After must win over the cooldown: %v", got)
	}
}

func TestOverBudgetDropsPaidSmart(t *testing.T) {
	kyiv, _ := time.LoadLocation("Europe/Kyiv")
	b := &budget{usd: 1.5}
	r, _ := newRouter(t, WithBudget(b, 1.0, kyiv))
	got := route(t, r, router.TaskReq{Kind: "message"})
	if slices.Contains(got, "or/smart-pricey") || slices.Contains(got, "or/vision") {
		t.Fatalf("paid smart models kept over budget: %v", got)
	}
	if got[0] != "claude/sonnet" {
		t.Fatalf("free smart model dropped: %v", got)
	}
	if want := time.Date(2026, 11, 3, 0, 0, 0, 0, kyiv); !b.since.Equal(want) {
		t.Fatalf("budget day starts %v, want %v", b.since, want)
	}
	if got := route(t, r, router.TaskReq{Kind: "message", Tier: "smart"}); !slices.Contains(got, "or/smart-pricey") {
		t.Fatalf("an explicit smart task keeps paid smart models: %v", got)
	}
}

func TestNoCandidate(t *testing.T) {
	r, _ := newRouter(t)
	_, err := r.Route(context.Background(), router.TaskReq{Privacy: "local-only", Vision: true})
	if !errors.Is(err, router.ErrNoCandidate) {
		t.Fatalf("err %v, want ErrNoCandidate", err)
	}
}
