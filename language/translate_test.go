package language

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

// scripted answers each request through fn and keeps the requests.
type scripted struct {
	mu   sync.Mutex
	reqs []provider.ChatRequest
	fn   func(ctx context.Context, keys map[texts.Key]string) (string, error)
}

func (*scripted) Name() string { return "p" }

func (p *scripted) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	keys := map[texts.Key]string{}
	if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &keys); err != nil {
		return provider.ChatResponse{}, err
	}
	s, err := p.fn(ctx, keys)
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: s}}, err
}

func (p *scripted) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

type oneModel struct{ kinds []router.TaskKind }

func (r *oneModel) Route(_ context.Context, t router.TaskReq) ([]router.Candidate, error) {
	r.kinds = append(r.kinds, t.Kind)
	return []router.Candidate{{Provider: "p", Model: "m"}}, nil
}

func (*oneModel) Report(router.Candidate, error) {}

// polish "translates" by keeping the placeholders and prefixing the words.
func polish(_ context.Context, keys map[texts.Key]string) (string, error) {
	out := map[texts.Key]string{}
	for k, v := range keys {
		out[k] = "PL " + v
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func newTranslator(t *testing.T, fn func(context.Context, map[texts.Key]string) (string, error)) (*Store, *clock, *scripted, *Translator) {
	t.Helper()
	s, c := newStore(t)
	p := &scripted{fn: fn}
	cat, err := texts.New(texts.WithPacks(s.Pack))
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranslator(s, cat, &oneModel{}, map[string]provider.Provider{"p": p}, WithTranslatorClock(c.time))
	return s, c, p, tr
}

func TestTranslateOncePerLanguage(t *testing.T) {
	s, _, p, tr := newTranslator(t, polish)
	ctx := context.Background()
	tr.Need("pl")
	tr.Need("pl")
	tr.sweep(ctx)
	tr.Need("pl")
	tr.sweep(ctx)
	if p.calls() != 1 {
		t.Fatalf("%d calls", p.calls())
	}
	if got, ok := s.Pack("pl", texts.ToastUsed); !ok || got != "PL "+texts.EnglishText(texts.ToastUsed) {
		t.Fatalf("pack %q, %v", got, ok)
	}
}

func TestShippedAndEnglishNeverTranslated(t *testing.T) {
	_, _, p, tr := newTranslator(t, polish)
	tr.Need("uk")
	tr.Need("en")
	tr.sweep(context.Background())
	if p.calls() != 0 {
		t.Fatalf("%d calls", p.calls())
	}
}

func TestStaleKeyOnly(t *testing.T) {
	s, _, p, tr := newTranslator(t, polish)
	ctx := context.Background()
	tr.Need("pl")
	tr.sweep(ctx)
	if _, err := s.db.Exec(`UPDATE packs SET source_hash = 'old' WHERE lang = 'pl' AND key = ?`, string(texts.ToastUsed)); err != nil {
		t.Fatal(err)
	}
	if err := s.loadPacks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Pack("pl", texts.ToastUsed); ok {
		t.Fatal("a stale text is still used")
	}
	tr.sweep(ctx)
	if p.calls() != 1 {
		t.Fatalf("translated with nobody to read it: %d calls", p.calls())
	}
	tr.Need("pl")
	tr.sweep(ctx)
	if p.calls() != 2 {
		t.Fatalf("%d calls", p.calls())
	}
	keys := map[texts.Key]string{}
	if err := json.Unmarshal([]byte(p.reqs[1].Messages[1].Content), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[texts.ToastUsed] == "" {
		t.Fatalf("sent %v", keys)
	}
}

func TestBadOutputRefused(t *testing.T) {
	cases := []struct {
		name  string
		bad   func(map[texts.Key]string)
		raw   string
		whole bool // the whole pack is refused
		key   texts.Key
	}{
		{name: "not JSON", raw: "sorry, no", whole: true},
		{name: "a missing approval key", bad: func(m map[texts.Key]string) { delete(m, texts.ButtonApprove) }, whole: true},
		{name: "an approval text with a bad placeholder", bad: func(m map[texts.Key]string) { m[texts.AskOpenPage] = "PL" }, whole: true},
		{name: "a dropped placeholder", bad: func(m map[texts.Key]string) { m[texts.ReplyResumed] = "PL" }, key: texts.ReplyResumed},
		{name: "a translated placeholder", bad: func(m map[texts.Key]string) { m[texts.ReplyResumed] = "PL {имя}" }, key: texts.ReplyResumed},
		{name: "an added address", bad: func(m map[texts.Key]string) { m[texts.ToastUsed] = "PL https://evil.example" }, key: texts.ToastUsed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, tr := newTranslator(t, func(_ context.Context, keys map[texts.Key]string) (string, error) {
				if tc.raw != "" {
					return tc.raw, nil
				}
				out := map[texts.Key]string{}
				for k, v := range keys {
					out[k] = "PL " + v
				}
				tc.bad(out)
				raw, err := json.Marshal(out)
				return string(raw), err
			})
			tr.Need("pl")
			tr.sweep(context.Background())
			if tc.whole {
				if _, ok := s.Pack("pl", texts.ToastExpired); ok {
					t.Fatal("the pack was kept")
				}
				return
			}
			if _, ok := s.Pack("pl", tc.key); ok {
				t.Fatalf("%s was kept", tc.key)
			}
			if _, ok := s.Pack("pl", texts.ToastExpired); !ok {
				t.Fatal("the good texts were dropped")
			}
		})
	}
}

func TestEchoOfEnglishRefused(t *testing.T) {
	s, _, _, tr := newTranslator(t, func(_ context.Context, keys map[texts.Key]string) (string, error) {
		raw, err := json.Marshal(keys)
		return string(raw), err
	})
	tr.Need("pl")
	tr.sweep(context.Background())
	if _, ok := s.Pack("pl", texts.ToastUsed); ok {
		t.Fatal("an echo of English was kept")
	}
}

func TestFencedJSONAccepted(t *testing.T) {
	s, _, _, tr := newTranslator(t, func(ctx context.Context, keys map[texts.Key]string) (string, error) {
		raw, err := polish(ctx, keys)
		return "```json\n" + raw + "\n```", err
	})
	tr.Need("pl")
	tr.sweep(context.Background())
	if _, ok := s.Pack("pl", texts.ToastUsed); !ok {
		t.Fatal("a fenced answer was refused")
	}
}

func TestRequestHoldsOnlyOurStrings(t *testing.T) {
	_, _, p, tr := newTranslator(t, polish)
	tr.Need("pl")
	tr.sweep(context.Background())
	req := p.reqs[0]
	if len(req.Tools) != 0 || req.Exec != nil || len(req.Messages) != 2 ||
		req.Messages[0].Role != provider.RoleSystem || req.Messages[1].Role != provider.RoleUser {
		t.Fatalf("request %+v", req)
	}
	if !strings.Contains(req.Messages[0].Content, "Polski") {
		t.Errorf("system %q", req.Messages[0].Content)
	}
	keys := map[texts.Key]string{}
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &keys); err != nil {
		t.Fatal(err)
	}
	for k, v := range keys {
		if texts.EnglishText(k) != v {
			t.Errorf("%s: %q is not our text", k, v)
		}
	}
}

func TestDailyCap(t *testing.T) {
	_, c, p, tr := newTranslator(t, func(context.Context, map[texts.Key]string) (string, error) {
		return "", errors.New("down")
	})
	ctx := context.Background()
	for range attemptsPerDay + 3 {
		tr.Need("pl")
		tr.sweep(ctx)
		c.now = c.now.Add(retryAfter + time.Second)
	}
	if p.calls() != attemptsPerDay {
		t.Fatalf("%d calls", p.calls())
	}
	c.now = c.now.Add(25 * time.Hour)
	tr.Need("pl")
	tr.sweep(ctx)
	if p.calls() != attemptsPerDay+1 {
		t.Fatalf("after a day: %d calls", p.calls())
	}
}

func TestRestartResumes(t *testing.T) {
	s, _, p, tr := newTranslator(t, polish)
	ctx := context.Background()
	tr.Need("pl")
	tr.sweep(ctx)
	fresh, err := New(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.Pack("pl", texts.ToastUsed); !ok {
		t.Fatal("a restart lost the pack")
	}
	cat, _ := texts.New(texts.WithPacks(fresh.Pack))
	again := NewTranslator(fresh, cat, &oneModel{}, map[string]provider.Provider{"p": p})
	again.Need("pl")
	again.sweep(ctx)
	if p.calls() != 1 {
		t.Fatalf("translated again: %d calls", p.calls())
	}
}

func TestRestartCarriesTheCap(t *testing.T) {
	s, c, p, tr := newTranslator(t, func(context.Context, map[texts.Key]string) (string, error) {
		return "", errors.New("down")
	})
	ctx := context.Background()
	for range attemptsPerDay {
		tr.Need("pl")
		tr.sweep(ctx)
		c.now = c.now.Add(retryAfter + time.Second)
	}
	cat, _ := texts.New(texts.WithPacks(s.Pack))
	again := NewTranslator(s, cat, &oneModel{}, map[string]provider.Provider{"p": p}, WithTranslatorClock(c.time))
	again.Need("pl")
	again.sweep(ctx)
	if p.calls() != attemptsPerDay {
		t.Fatalf("a restart reset the cap: %d calls", p.calls())
	}
}

func TestNeverHoldsATurn(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	_, _, _, tr := newTranslator(t, func(ctx context.Context, keys map[texts.Key]string) (string, error) {
		entered <- struct{}{}
		<-release
		return polish(ctx, keys)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()
	tr.Need("pl")
	<-entered
	for range 5 { // none of these may block while the call hangs
		tr.Need("pl")
	}
	close(release)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

func TestUnlistedLanguageNeverTranslated(t *testing.T) {
	_, _, p, tr := newTranslator(t, polish)
	tr.Need("xx")
	tr.sweep(context.Background())
	if p.calls() != 0 {
		t.Fatalf("%d calls", p.calls())
	}
}

func TestModelFailureKeepsEnglishForTheCatalogue(t *testing.T) {
	s, _, _, tr := newTranslator(t, func(context.Context, map[texts.Key]string) (string, error) {
		return "", errors.New("down")
	})
	tr.Need("pl")
	tr.sweep(context.Background())
	cat, _ := texts.New(texts.WithPacks(s.Pack))
	if got := cat.Label("pl", texts.ToastUsed, nil); got != texts.EnglishText(texts.ToastUsed) {
		t.Fatalf("%q", got)
	}
}

func TestOneCallPerAttempt(t *testing.T) {
	s, _ := newStore(t)
	p := &scripted{fn: func(context.Context, map[texts.Key]string) (string, error) { return "", errors.New("down") }}
	cat, _ := texts.New(texts.WithPacks(s.Pack))
	two := twoModels{}
	tr := NewTranslator(s, cat, two, map[string]provider.Provider{"p": p})
	tr.Need("pl")
	tr.sweep(context.Background())
	if p.calls() != 1 {
		t.Fatalf("an attempt made %d calls", p.calls())
	}
}

type twoModels struct{}

func (twoModels) Route(context.Context, router.TaskReq) ([]router.Candidate, error) {
	return []router.Candidate{{Provider: "p", Model: "cheap"}, {Provider: "p", Model: "smart"}}, nil
}

func (twoModels) Report(router.Candidate, error) {}

func TestCorrectTextEqualToEnglishIsKept(t *testing.T) {
	// "Details" is German too: a small sweep is not an echo.
	source := map[texts.Key]string{texts.ButtonDetails: "Details"}
	ok, outcome, _ := accept(source, `{"button.details":"Details"}`)
	if outcome != outcomeDone || ok[texts.ButtonDetails] != "Details" {
		t.Fatalf("%s, %v", outcome, ok)
	}
}

func TestEchoedApprovalsAreRefused(t *testing.T) {
	out := map[texts.Key]string{}
	source := map[texts.Key]string{}
	for _, k := range texts.Keys() {
		source[k] = texts.EnglishText(k)
		if texts.IsApproval(k) {
			out[k] = source[k]
		} else {
			out[k] = "PL " + source[k]
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := accept(source, string(raw)); outcome != outcomeRefused {
		t.Fatalf("echoed approvals: %s", outcome)
	}
}

func TestEmptyAnswerIsRefused(t *testing.T) {
	source := map[texts.Key]string{texts.ToastUsed: "Already handled."}
	for _, answer := range []string{"{}", "null"} {
		if _, outcome, _ := accept(source, answer); outcome != outcomeRefused {
			t.Errorf("%s: %s", answer, outcome)
		}
	}
}

func TestReasonNeverQuotesAModelsKey(t *testing.T) {
	source := map[texts.Key]string{texts.ToastUsed: "Already handled.", texts.ToastExpired: "Expired."}
	answer := `{"toast.used":"","toast.expired":"Wygasło.","!injected":"x"}`
	_, outcome, reason := accept(source, answer)
	if outcome != outcomePartial || strings.Contains(reason, "injected") {
		t.Fatalf("%s, %q", outcome, reason)
	}
}

func TestMalformedStoredLanguageIsNotTranslated(t *testing.T) {
	_, _, p, tr := newTranslator(t, polish)
	tr.Need("PL")
	tr.sweep(context.Background())
	if p.calls() != 0 {
		t.Fatalf("%d calls", p.calls())
	}
}

// The owner's ruling: lazy. A language in the table with nobody being written to costs nothing, so a
// removed person's language needs no special case.
func TestNobodyWrittenToCostsNothing(t *testing.T) {
	s, _, p, tr := newTranslator(t, polish)
	if err := s.Seen(context.Background(), "gone", "pl"); err != nil {
		t.Fatal(err)
	}
	tr.sweep(context.Background())
	if p.calls() != 0 {
		t.Fatalf("%d calls", p.calls())
	}
}

func TestRunTranslatesOnlyWhenNeeded(t *testing.T) {
	entered := make(chan struct{}, 1)
	s, _, _, tr := newTranslator(t, func(ctx context.Context, keys map[texts.Key]string) (string, error) {
		entered <- struct{}{}
		return polish(ctx, keys)
	})
	if err := s.Seen(context.Background(), "a", "pl"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()
	tr.Need("pl")
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

// Final review: a short outage must not use up the day on a person's first messages.
func TestFailureRetriesLaterNotAtOnce(t *testing.T) {
	_, c, p, tr := newTranslator(t, func(context.Context, map[texts.Key]string) (string, error) {
		return "", errors.New("down")
	})
	ctx := context.Background()
	for range 5 {
		tr.Need("pl")
		tr.sweep(ctx)
	}
	if p.calls() != 1 {
		t.Fatalf("%d calls within one moment", p.calls())
	}
	c.now = c.now.Add(retryAfter + time.Second)
	tr.Need("pl")
	tr.sweep(ctx)
	if p.calls() != 2 {
		t.Fatalf("after the wait: %d calls", p.calls())
	}
}

func TestAskedLanguagesAreCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.Seen(ctx, "a", "pl"); err != nil {
		t.Fatal(err)
	}
	if err := s.Seen(ctx, "a", "pl"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Choose(ctx, "a", "uk"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Value(t, "ganglion.language.asked", map[string]string{"lang": "pl", "how": "channel"}); got != 1 {
		t.Errorf("channel: %v", got)
	}
	if got := rec.Value(t, "ganglion.language.asked", map[string]string{"lang": "uk", "how": "chosen"}); got != 1 {
		t.Errorf("chosen: %v", got)
	}
}
