package rules_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// clock is a settable clock.
type clock struct{ t time.Time }

func newClock() *clock                   { return &clock{t: t0} }
func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// budget reports a fixed spend and records the moment it was asked about.
type budget struct {
	usd   float64
	since time.Time
}

func spent(usd float64) *budget { return &budget{usd: usd} }

func (b *budget) SpentSince(_ context.Context, t time.Time) (float64, error) {
	b.since = t
	return b.usd, nil
}

// testModels are sonnet and haiku on provider p: tools, 200k, free.
func testModels() []router.Model {
	caps := router.ModelCaps{Tools: true, ContextTokens: 200_000}
	return []router.Model{
		{ID: "sonnet", Provider: "p", Name: "sonnet", Caps: caps},
		{ID: "haiku", Provider: "p", Name: "haiku", Caps: caps},
	}
}

// paid is a model with a price on provider q.
func paid(id string) router.Model {
	return router.Model{ID: id, Provider: "q", Name: id, Caps: router.ModelCaps{Tools: true, ContextTokens: 200_000, PriceInMTok: 0.5, PriceOutMTok: 0.5}}
}

func chatSonnetHaiku() map[router.Purpose][]router.Choice {
	return map[router.Purpose][]router.Choice{
		router.PurposeChat: {{Model: "sonnet", Effort: router.EffortMedium}, {Model: "haiku"}},
	}
}

func mustNew(t *testing.T, ms []router.Model, purposes map[router.Purpose][]router.Choice, opts ...rules.Option) *rules.Router {
	t.Helper()
	r, err := rules.New(ms, purposes, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// models routes task and returns model/effort per candidate.
func models(t *testing.T, r *rules.Router, task router.TaskReq) []string {
	t.Helper()
	cs, err := r.Route(context.Background(), task)
	if err != nil {
		t.Fatalf("Route(%+v): %v", task, err)
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Model + "/" + string(c.Effort)
	}
	return out
}

func TestRouteFollowsThePurposeOrder(t *testing.T) {
	r := mustNew(t, testModels(), map[router.Purpose][]router.Choice{
		router.PurposeChat:  {{Model: "sonnet", Effort: router.EffortMedium}, {Model: "haiku"}},
		router.PurposeCheck: {{Model: "haiku", Effort: router.EffortLow}},
	})
	got := models(t, r, router.TaskReq{Purpose: router.PurposeChat})
	if want := []string{"sonnet/medium", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("chat = %v, want %v", got, want)
	}
	got = models(t, r, router.TaskReq{Purpose: router.PurposeCheck})
	if want := []string{"haiku/low"}; !slices.Equal(got, want) {
		t.Fatalf("check = %v, want %v", got, want)
	}
}

func TestCoolingMovesToTheEnd(t *testing.T) {
	clk := newClock()
	r := mustNew(t, testModels(), chatSonnetHaiku(), rules.WithClock(clk.now))
	r.Report(router.Candidate{Provider: "p", Model: "sonnet"}, &provider.Error{Kind: provider.KindRateLimit, RetryAfter: time.Minute})
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"haiku/", "sonnet/medium"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	clk.advance(2 * time.Minute)
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"sonnet/medium", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("after cooling: got %v, want %v", got, want)
	}
}

func TestOverBudgetChatKeepsFreeModelsOnly(t *testing.T) {
	r := mustNew(t, append(testModels(), paid("flash")), map[router.Purpose][]router.Choice{
		router.PurposeChat:    {{Model: "flash"}, {Model: "haiku"}},
		router.PurposeCompact: {{Model: "flash"}},
	}, rules.WithBudget(spent(5), 1, time.UTC))
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat, Turn: true}), []string{"haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("chat over budget = %v, want %v", got, want)
	}
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeCompact}), []string{"flash/"}; !slices.Equal(got, want) {
		t.Fatalf("compact over budget = %v, want %v", got, want)
	}
}

// R-RTR-6: the budget cuts a turn, whatever purpose a skill moved it to,
// and never the housekeeping outside a turn.
func TestOverBudgetCutsATurnWhateverItsPurpose(t *testing.T) {
	r := mustNew(t, append(testModels(), paid("flash")), map[router.Purpose][]router.Choice{
		router.PurposeChat:    {{Model: "flash"}, {Model: "haiku"}},
		router.PurposeCheck:   {{Model: "flash"}, {Model: "haiku"}},
		router.PurposeCompact: {{Model: "flash"}, {Model: "haiku"}},
	}, rules.WithBudget(spent(5), 1, time.UTC))
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeCheck, Turn: true}), []string{"haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("a turn re-routed to check over budget = %v, want %v", got, want)
	}
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeCompact}), []string{"flash/", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("a compaction over budget = %v, want %v", got, want)
	}
}

func TestFiltersStillApply(t *testing.T) {
	r := mustNew(t, testModels(), chatSonnetHaiku())
	if _, err := r.Route(context.Background(), router.TaskReq{Purpose: router.PurposeChat, Privacy: "local-only"}); !errors.Is(err, router.ErrNoCandidate) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Route(context.Background(), router.TaskReq{Purpose: router.PurposeCode}); !errors.Is(err, router.ErrNoCandidate) {
		t.Fatalf("code with no list: err = %v", err)
	}
}

func TestToolsVisionAndLongContextFilter(t *testing.T) {
	ms := []router.Model{
		{ID: "small", Provider: "p", Name: "small", Caps: router.ModelCaps{Tools: true, ContextTokens: 32_000}},
		{ID: "plain", Provider: "p", Name: "plain", Caps: router.ModelCaps{ContextTokens: 200_000}},
		{ID: "eyes", Provider: "p", Name: "eyes", Caps: router.ModelCaps{Tools: true, Vision: true, ContextTokens: 200_000}},
	}
	r := mustNew(t, ms, map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "small"}, {Model: "plain"}, {Model: "eyes"}}})
	for task, want := range map[router.TaskReq][]string{
		{Purpose: router.PurposeChat, Tools: true}:     {"small/", "eyes/"},
		{Purpose: router.PurposeChat, Vision: true}:    {"eyes/"},
		{Purpose: router.PurposeChat, Context: "long"}: {"plain/", "eyes/"},
	} {
		if got := models(t, r, task); !slices.Equal(got, want) {
			t.Errorf("%+v: got %v, want %v", task, got, want)
		}
	}
}

func TestLocalOnlyKeepsLocalModels(t *testing.T) {
	ms := append(testModels(), router.Model{ID: "local", Provider: "ollama", Name: "qwen", Caps: router.ModelCaps{Tools: true, Local: true}})
	r := mustNew(t, ms, map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "sonnet"}, {Model: "local"}}})
	if got := models(t, r, router.TaskReq{Purpose: router.PurposeChat, Privacy: "local-only"}); !slices.Equal(got, []string{"qwen/"}) {
		t.Fatalf("local-only = %v", got)
	}
}

func TestOverBudgetKeepsPaidLocalModels(t *testing.T) {
	local := router.Model{ID: "box", Provider: "ollama", Name: "box", Caps: router.ModelCaps{Local: true, PriceInMTok: 1, PriceOutMTok: 1}}
	r := mustNew(t, []router.Model{paid("flash"), local}, map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "flash"}, {Model: "box"}}},
		rules.WithBudget(spent(2), 1, time.UTC))
	if got := models(t, r, router.TaskReq{Purpose: router.PurposeChat, Turn: true}); !slices.Equal(got, []string{"box/"}) {
		t.Fatalf("got %v", got)
	}
}

func TestBudgetDayStartsInTheZone(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	b := spent(0.5)
	clk := newClock()
	r := mustNew(t, append(testModels(), paid("flash")), map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "flash"}, {Model: "haiku"}}},
		rules.WithClock(clk.now), rules.WithBudget(b, 1, kyiv))
	if got := models(t, r, router.TaskReq{Purpose: router.PurposeChat, Turn: true}); !slices.Equal(got, []string{"flash/", "haiku/"}) {
		t.Fatalf("under budget: %v", got)
	}
	if want := time.Date(2026, 11, 3, 0, 0, 0, 0, kyiv); !b.since.Equal(want) {
		t.Fatalf("budget day starts %v, want %v", b.since, want)
	}
}

func TestCandidateCarriesPricesAndEffort(t *testing.T) {
	r := mustNew(t, []router.Model{paid("flash")}, map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "flash", Effort: router.EffortHigh}}})
	cs, err := r.Route(context.Background(), router.TaskReq{Purpose: router.PurposeChat})
	if err != nil || len(cs) != 1 || cs[0] != (router.Candidate{Provider: "q", Model: "flash", Effort: router.EffortHigh, PriceInMTok: 0.5, PriceOutMTok: 0.5}) {
		t.Fatalf("candidates %+v, %v", cs, err)
	}
}

func TestALongerRetryAfterWins(t *testing.T) {
	clk := newClock()
	r := mustNew(t, testModels(), chatSonnetHaiku(), rules.WithClock(clk.now), rules.WithCooldown(time.Minute))
	r.Report(router.Candidate{Provider: "p", Model: "sonnet"}, &provider.Error{Kind: provider.KindRateLimit, RetryAfter: 5 * time.Minute})
	r.Report(router.Candidate{Provider: "p", Model: "haiku"}, &provider.Error{Kind: provider.KindUnavailable})
	clk.advance(2 * time.Minute)
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"haiku/", "sonnet/medium"}; !slices.Equal(got, want) {
		t.Fatalf("a longer Retry-After must win over the cooldown: %v", got)
	}
}

func TestAuthErrorCoolsTheWholeProvider(t *testing.T) {
	clk := newClock()
	ms := append(testModels(), paid("flash"))
	r := mustNew(t, ms, map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "sonnet"}, {Model: "haiku"}, {Model: "flash"}}},
		rules.WithClock(clk.now))
	r.Report(router.Candidate{Provider: "p", Model: "sonnet"}, &provider.Error{Kind: provider.KindAuth})
	clk.advance(9 * time.Minute)
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"flash/", "sonnet/", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("a wrong key or login is the provider's, all its models wait 10 min: %v", got)
	}
	clk.advance(2 * time.Minute)
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"sonnet/", "haiku/", "flash/"}; !slices.Equal(got, want) {
		t.Fatalf("auth cooldown did not end: %v", got)
	}
}

func TestNewRefusesADanglingChoice(t *testing.T) {
	if _, err := rules.New(testModels(), map[router.Purpose][]router.Choice{router.PurposeChat: {{Model: "opus"}}}); err == nil {
		t.Fatal("a purpose naming no model must fail")
	}
}
