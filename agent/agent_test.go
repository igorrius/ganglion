package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/turns"
)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// fakeProvider answers from a script; an empty script answers "ok".
type fakeProvider struct {
	name   string
	mu     sync.Mutex
	script []func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)
	reqs   []provider.ChatRequest
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	var step func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)
	if len(f.script) > 0 {
		step, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if step == nil {
		return answerText("ok"), nil
	}
	return step(ctx, req)
}

func (f *fakeProvider) push(steps ...func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, steps...)
}

func (f *fakeProvider) requests() []provider.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.ChatRequest(nil), f.reqs...)
}

func answerText(s string) provider.ChatResponse {
	return provider.ChatResponse{Message: provider.Message{Role: "assistant", Content: s}, Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}}
}

func say(s string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) { return answerText(s), nil }
}

func fail(kind provider.ErrorKind) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, &provider.Error{Provider: "x", Kind: kind, Msg: string(kind)}
	}
}

func callTool(name string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: fmt.Sprintf("call_%d", len(req.Messages)), Name: name, Args: json.RawMessage(`{}`)}}}, Usage: provider.Usage{InputTokens: 50, OutputTokens: 5}}, nil
	}
}

func hang(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	<-ctx.Done()
	return provider.ChatResponse{}, &provider.Error{Provider: "x", Kind: provider.KindTimeout, Msg: "deadline"}
}

// states is an in-memory conversation state store.
type states struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *states) State(_ context.Context, conv string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[conv], nil
}

func (s *states) apply(st queue.StateUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Conv != "" {
		s.m[st.Conv] = st.Data
	}
}

type fakeTools struct{ calls []string }

func (f *fakeTools) List() []provider.Tool {
	return []provider.Tool{{Name: "clock", Description: "time", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (f *fakeTools) Call(_ context.Context, c provider.ToolCall) (string, error) {
	f.calls = append(f.calls, c.Name)
	return "12:00", nil
}

type env struct {
	a          *Agent
	smart      *fakeProvider // p1/smart: tried first for messages
	cheap      *fakeProvider // p2/cheap: fallback, and compaction
	states     *states
	turns      *turns.Log
	now        time.Time
	router     *rules.Router
	archive    *Archive
	logs       *bytes.Buffer
	persona    string
	personaErr error
}

func newEnv(t *testing.T, cfg Config, tools Tools) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tl, err := turns.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	arch, err := NewArchive(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{logs: &bytes.Buffer{}, archive: arch, smart: &fakeProvider{name: "p1"}, cheap: &fakeProvider{name: "p2"}, states: &states{m: map[string]json.RawMessage{}}, turns: tl, now: t0}
	e.router, err = rules.New([]router.Model{
		{Provider: "p1", Name: "smart", Tier: "smart", Caps: router.ModelCaps{Tools: true, PriceInMTok: 1, PriceOutMTok: 2}},
		{Provider: "p2", Name: "cheap", Tier: "cheap", Caps: router.ModelCaps{Tools: true}},
	}, rules.WithClock(func() time.Time { return e.now }))
	if err != nil {
		t.Fatal(err)
	}
	if tools == nil {
		tools = NoTools{}
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	e.a = New(cfg, Deps{
		States: e.states, Router: e.router, Tools: tools, Turns: tl, Archive: arch,
		Providers: map[string]provider.Provider{"p1": e.smart, "p2": e.cheap},
		Alerts: alert.Admins{List: func(context.Context) ([]string, error) { return []string{"telegram:user:1"}, nil }, Private: func(p string) (string, string, bool) {
			id, ok := strings.CutPrefix(p, "telegram:user:")
			return "telegram", "telegram:" + id, ok
		}},
		Now:     func() time.Time { return e.now },
		Persona: func() (string, error) { return e.persona, e.personaErr },
		Log:     slog.New(slog.NewTextHandler(e.logs, nil)),
	})
	return e
}

var seq int

func message(from, name, txt string) event.Event {
	seq++
	p, _ := json.Marshal(event.Message{Text: txt, From: from, Name: name})
	return event.Event{ID: fmt.Sprintf("01EV%04d", seq), IdemKey: fmt.Sprintf("telegram:7001:%d", seq), Source: "telegram",
		Conv: "telegram:42", Kind: "message", Payload: p, At: t0}
}

// turn handles e and applies its state update, as Complete would.
func (e *env) turn(t *testing.T, ev event.Event) []event.Outgoing {
	t.Helper()
	out, st, err := e.a.Handle(context.Background(), ev)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	e.states.apply(st)
	return out
}

func (e *env) state(t *testing.T) State {
	t.Helper()
	st, err := DecodeState(e.states.m["telegram:42"])
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTurnRepliesAndStoresHistory(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("Привет, Игорь!"))
	ev := message("telegram:user:42", "Igor", "привет")
	out := e.turn(t, ev)
	if len(out) != 1 || out[0].Text != "Привет, Игорь!" || out[0].IdemKey != ev.IdemKey+":reply:0" || out[0].Transport != "telegram" || out[0].Conv != "telegram:42" {
		t.Fatalf("out %+v", out)
	}
	st := e.state(t)
	if len(st.Messages) != 2 || st.Messages[0].Content != "Igor: привет" || st.Messages[1].Content != "Привет, Игорь!" {
		t.Fatalf("state %+v", st)
	}
	tr, err := e.turns.Get(context.Background(), ev.ID)
	if err != nil || tr.Outcome != "ok" || tr.Steps != 1 || tr.InputTokens != 100 || tr.OutputTokens != 10 || tr.Principal != "telegram:user:42" {
		t.Fatalf("turn %+v, %v", tr, err)
	}
	if want := (100*1.0 + 10*2.0) / 1e6; tr.CostUSD != want {
		t.Fatalf("cost %v, want %v", tr.CostUSD, want)
	}
}

func TestHistoryReachesTheModel(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.persona = "SYS"
	e.turn(t, message("telegram:user:42", "Igor", "первое"))
	e.turn(t, message("telegram:user:42", "Igor", "второе"))
	msgs := e.smart.requests()[1].Messages
	got := []string{}
	for _, m := range msgs {
		got = append(got, string(m.Role)+":"+m.Content)
	}
	want := "system:" + CoreRules + "\n\nSYS|user:Igor: первое|assistant:ok|user:Igor: второе"
	if strings.Join(got, "|") != want {
		t.Fatalf("messages %q\nwant %q", strings.Join(got, "|"), want)
	}
}

func TestSummaryGoesBeforeMessages(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.persona = "SYS"
	raw, _ := json.Marshal(State{V: 1, Session: "s", Started: t0, Last: t0, Summary: "they talked about the boiler",
		Messages: []provider.Message{{Role: "user", Content: "Igor: a"}, {Role: "assistant", Content: "b"}}})
	e.states.m["telegram:42"] = raw
	e.turn(t, message("telegram:user:42", "Igor", "c"))
	msgs := e.smart.requests()[0].Messages
	if msgs[0].Content != CoreRules+"\n\nSYS" || msgs[1].Role != "system" || msgs[1].Content != "Summary of the conversation so far:\nthey talked about the boiler" || msgs[2].Content != "Igor: a" {
		t.Fatalf("order %+v", msgs)
	}
}

func TestToolCallLoop(t *testing.T) {
	tools := &fakeTools{}
	e := newEnv(t, Config{}, tools)
	e.smart.push(callTool("clock"), say("It is noon."))
	out := e.turn(t, message("telegram:user:42", "Igor", "time?"))
	if out[0].Text != "It is noon." || len(tools.calls) != 1 {
		t.Fatalf("out %+v, calls %v", out, tools.calls)
	}
	second := e.smart.requests()[1]
	last := second.Messages[len(second.Messages)-1]
	if len(second.Tools) != 1 || last.Role != "tool" || last.Content != "12:00" || last.ToolCallID == "" {
		t.Fatalf("second request %+v", second)
	}
	st := e.state(t)
	if len(st.Messages) != 4 || len(st.Messages[1].ToolCalls) != 1 || st.Messages[2].Role != "tool" {
		t.Fatalf("state %+v", st.Messages)
	}
}

func TestStepLimit(t *testing.T) {
	e := newEnv(t, Config{MaxSteps: 3}, &fakeTools{})
	for range 5 {
		e.smart.push(callTool("clock"))
	}
	ev := message("telegram:user:42", "Igor", "loop")
	out := e.turn(t, ev)
	if len(e.smart.requests()) != 3 || out[0].Text != "Sorry, I could not finish that." {
		t.Fatalf("%d calls, out %+v", len(e.smart.requests()), out)
	}
	if tr, _ := e.turns.Get(context.Background(), ev.ID); tr.Outcome != "step_limit" || tr.Steps != 3 {
		t.Fatalf("turn %+v", tr)
	}
}

func TestFallbackOnRateLimit(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(fail(provider.KindRateLimit))
	e.cheap.push(say("from cheap"))
	ev := message("telegram:user:42", "Igor", "hi")
	if out := e.turn(t, ev); out[0].Text != "from cheap" {
		t.Fatalf("out %+v", out)
	}
	tr, _ := e.turns.Get(context.Background(), ev.ID)
	if len(tr.Route) != 2 || tr.Route[0] != (turns.Attempt{Provider: "p1", Model: "smart", Error: "rate_limit"}) || tr.Route[1].Error != "" {
		t.Fatalf("route %+v", tr.Route)
	}
	cs, _ := e.router.Route(context.Background(), router.TaskReq{Kind: "message"})
	if cs[0].Provider != "p2" {
		t.Fatalf("the router did not hear about the rate limit: %+v", cs)
	}
}

func TestFallbackOnAuthToo(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(fail(provider.KindAuth))
	e.cheap.push(say("from cheap"))
	if out := e.turn(t, message("telegram:user:42", "Igor", "hi")); out[0].Text != "from cheap" {
		t.Fatalf("a wrong key on one provider must not stop the others: %+v", out)
	}
}

func TestAllModelsDownApologises(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindRateLimit))
	ev := message("telegram:user:42", "Igor", "hi")
	out := e.turn(t, ev)
	if out[0].Text != "Sorry, no model is reachable right now. Please try again in a few minutes." {
		t.Fatalf("out %+v", out)
	}
	if tr, _ := e.turns.Get(context.Background(), ev.ID); tr.Outcome != "no_model" {
		t.Fatalf("turn %+v", tr)
	}
	if st := e.state(t); len(st.Messages) != 1 || st.Messages[0].Content != "Igor: hi" {
		t.Fatalf("state %+v", st.Messages)
	}
}

func TestCallTimeoutMovesOn(t *testing.T) {
	e := newEnv(t, Config{CallTimeout: 50 * time.Millisecond}, nil)
	e.smart.push(hang)
	e.cheap.push(say("from cheap"))
	start := time.Now()
	if out := e.turn(t, message("telegram:user:42", "Igor", "hi")); out[0].Text != "from cheap" {
		t.Fatalf("out %+v", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the hung call held the turn")
	}
}

func TestCancelledTurnReturnsError(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(hang)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, _, err := e.a.Handle(ctx, message("telegram:user:42", "Igor", "hi")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle on shutdown = %v, want context.Canceled so the event is retried", err)
	}
}

func TestCompaction(t *testing.T) {
	e := newEnv(t, Config{HistoryTokens: 60, KeepRecent: 4}, &fakeTools{})
	long := strings.Repeat("слово ", 20)
	e.turn(t, message("telegram:user:42", "Igor", long))
	e.smart.push(callTool("clock"), say("answer"))
	e.cheap.push(say("SUMMARY"))
	e.turn(t, message("telegram:user:42", "Igor", long))
	st := e.state(t)
	if st.Summary != "SUMMARY" {
		t.Fatalf("summary %q", st.Summary)
	}
	if st.Messages[0].Role == "tool" {
		t.Fatal("kept history starts with a tool result cut from its call")
	}
	if n := len(st.Messages); n < 4 || n > 5 {
		t.Fatalf("kept %d messages, want KeepRecent (moved back to keep a tool call whole)", n)
	}
	creq := e.cheap.requests()[0]
	if !strings.Contains(creq.Messages[len(creq.Messages)-1].Content, "Igor: слово") {
		t.Fatalf("compaction prompt %+v", creq.Messages)
	}
}

func TestCompactionFailureKeepsHistory(t *testing.T) {
	e := newEnv(t, Config{HistoryTokens: 10, KeepRecent: 2}, nil)
	e.turn(t, message("telegram:user:42", "Igor", strings.Repeat("a ", 50)))
	before := len(e.state(t).Messages)
	e.cheap.push(fail(provider.KindUnavailable))
	e.smart.push(say("ok"), fail(provider.KindUnavailable))
	e.turn(t, message("telegram:user:42", "Igor", "next"))
	st := e.state(t)
	if st.Summary != "" || len(st.Messages) != before+2 {
		t.Fatalf("after a failed compaction: summary %q, %d messages", st.Summary, len(st.Messages))
	}
}

func TestDailyLimitAlertsAdminsOnce(t *testing.T) {
	e := newEnv(t, Config{DailyTurns: 1}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "one"))
	o1 := e.turn(t, message("telegram:user:42", "Igor", "two"))
	o2 := e.turn(t, message("telegram:user:42", "Igor", "three"))
	if len(o1) != 2 || o1[0].Text != "Daily limit reached; the bot answers again tomorrow." {
		t.Fatalf("over the limit: %+v", o1)
	}
	if o1[1].Conv != "telegram:1" || o1[1].IdemKey != "alert:daily-limit:2026-11-03:telegram:user:42:telegram:user:1" || o1[1].IdemKey != o2[1].IdemKey {
		t.Fatalf("alerts %+v / %+v", o1[1], o2[1])
	}
	if len(e.smart.requests()) != 1 {
		t.Fatal("the model was called over the limit")
	}
	if st := e.state(t); len(st.Messages) != 2 {
		t.Fatalf("a refused message entered history: %d messages", len(st.Messages))
	}
	e.now = t0.Add(24 * time.Hour)
	if out := e.turn(t, message("telegram:user:42", "Igor", "tomorrow")); out[0].Text != "ok" {
		t.Fatalf("next day %+v", out)
	}
}

func TestRerunSameEventSameResult(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	ev := message("telegram:user:42", "Igor", "hi")
	o1, s1, err1 := e.a.Handle(context.Background(), ev)
	o2, s2, err2 := e.a.Handle(context.Background(), ev)
	if err1 != nil || err2 != nil || o1[0].IdemKey != o2[0].IdemKey || string(s1.Data) != string(s2.Data) {
		t.Fatalf("rerun differs: %+v %+v", o1, o2)
	}
	tot, _ := e.turns.Totals(context.Background(), t0.Add(-time.Hour), "", "")
	if tot.Turns != 1 {
		t.Fatalf("%d turns rows, want 1", tot.Turns)
	}
}

func TestNonMessageIgnored(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	out, st, err := e.a.Handle(context.Background(), event.Event{ID: "x", Kind: "cron.tick", Conv: "cron:x"})
	if err != nil || len(out) != 0 || st.Conv != "" || len(e.smart.requests()) != 0 {
		t.Fatalf("non-message: %+v %+v %v", out, st, err)
	}
}

func TestDailyLimitIgnoresApologiesAndCommands(t *testing.T) {
	e := newEnv(t, Config{DailyTurns: 1}, nil)
	e.smart.push(fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	e.turn(t, message("telegram:user:42", "Igor", "during an outage"))
	e.turn(t, message("telegram:user:42", "Igor", "/sessions"))
	if out := e.turn(t, message("telegram:user:42", "Igor", "back?")); out[0].Text != "ok" {
		t.Fatalf("an apology or a command used up the quota: %+v", out)
	}
	ev := message("telegram:user:42", "Igor", "over")
	if out := e.turn(t, ev); out[0].Text != replyDailyLimit {
		t.Fatalf("over the limit: %+v", out)
	}
	if tr, _ := e.turns.Get(context.Background(), ev.ID); tr.Outcome != "daily_limit" {
		t.Fatalf("outcome %q", tr.Outcome)
	}
}

func TestRerunAtLimitBoundaryNotRefused(t *testing.T) {
	e := newEnv(t, Config{DailyTurns: 1}, nil)
	ev := message("telegram:user:42", "Igor", "the only turn today")
	e.turn(t, ev)
	out, _, err := e.a.Handle(context.Background(), ev) // the same event again, as after a crash
	if err != nil || out[0].Text != "ok" {
		t.Fatalf("a rerun counted its own earlier row: %+v, %v", out, err)
	}
}

func TestEmptyAnswerMovesOn(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say(""))
	e.cheap.push(say("from cheap"))
	if out := e.turn(t, message("telegram:user:42", "Igor", "hi")); out[0].Text != "from cheap" {
		t.Fatalf("an empty answer ended the turn: %+v", out)
	}
	for _, m := range e.state(t).Messages {
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatal("an empty assistant message entered history")
		}
	}
}

func TestFailedCallLogsProviderMessage(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, &provider.Error{Provider: "p1", Kind: provider.KindBadRequest, Status: 404, Msg: "model not found"}
	})
	e.turn(t, message("telegram:user:42", "Igor", "секрет"))
	logs := e.logs.String()
	if !strings.Contains(logs, "status=404") || !strings.Contains(logs, `msg="model not found"`) || strings.Contains(logs, "секрет") {
		t.Fatalf("logs %s", logs)
	}
}

func TestTraceRecordedWhenTheTurnContextRunsOut(t *testing.T) {
	e := newEnv(t, Config{HistoryTokens: 5, KeepRecent: 1}, nil)
	e.turn(t, message("telegram:user:42", "Igor", strings.Repeat("a ", 40)))
	e.cheap.push(hang) // compaction hangs until the turn's context ends
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ev := message("telegram:user:42", "Igor", strings.Repeat("b ", 40)) // long enough to compact again
	if _, _, err := e.a.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.turns.Get(context.Background(), ev.ID); err != nil {
		t.Fatalf("the trace was lost with the context: %v", err)
	}
}

func TestNoPersonaUsesTheNeutralDefault(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "hi"))
	sys := e.smart.requests()[0].Messages[0].Content
	if sys != CoreRules+"\n\n"+DefaultPersona {
		t.Fatalf("system prompt %q", sys)
	}
	if strings.Contains(strings.ToLower(CoreRules+DefaultPersona), "family") {
		t.Fatal("a persona is hard-coded in the core")
	}
}

func TestPersonaIsReadEveryTurn(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.persona = "You are Jarvis."
	e.turn(t, message("telegram:user:42", "Igor", "one"))
	e.persona = "You are Friday."
	e.turn(t, message("telegram:user:42", "Igor", "two"))
	if got := e.smart.requests()[1].Messages[0].Content; !strings.HasSuffix(got, "You are Friday.") {
		t.Fatalf("the edited persona did not reach the next turn: %q", got)
	}
}

func TestUnreadablePersonaFallsBackToDefault(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.personaErr = errors.New("permission denied")
	if out := e.turn(t, message("telegram:user:42", "Igor", "hi")); out[0].Text != "ok" {
		t.Fatalf("out %+v", out)
	}
	if got := e.smart.requests()[0].Messages[0].Content; !strings.HasSuffix(got, DefaultPersona) {
		t.Fatalf("system prompt %q", got)
	}
	if !strings.Contains(e.logs.String(), "persona") {
		t.Fatal("the unreadable persona was not logged")
	}
}

func TestRepliesCarryTheRef(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	ev := message("telegram:user:42", "Igor", "hi")
	p, _ := json.Marshal(event.Message{Text: "hi", From: "telegram:user:42", Name: "Igor", Ref: "77"})
	ev.Payload = p
	if out := e.turn(t, ev); out[0].ReplyTo != "77" {
		t.Fatalf("reply_to %q, want 77", out[0].ReplyTo)
	}
	e.smart.push(fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	ev2 := message("telegram:user:42", "Igor", "again")
	p2, _ := json.Marshal(event.Message{Text: "again", From: "telegram:user:42", Ref: "78"})
	ev2.Payload = p2
	if out := e.turn(t, ev2); out[0].ReplyTo != "78" {
		t.Fatalf("the apology does not reply to its message: %q", out[0].ReplyTo)
	}
}

func TestEveryTurnLogsOneInfoLine(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.smart.push(say("Секретный ответ"))
	e.turn(t, message("telegram:user:42", "Igor", "секретный вопрос"))
	var line string
	for l := range strings.SplitSeq(e.logs.String(), "\n") {
		if strings.Contains(l, `msg="agent: turn"`) {
			line = l
		}
	}
	for _, want := range []string{"level=INFO", "conv=telegram:42", "principal=telegram:user:42", "outcome=ok", "steps=1",
		"model=p1/smart", "in_tokens=100", "out_tokens=10", "cost_usd=", "ms="} {
		if !strings.Contains(line, want) {
			t.Errorf("turn log lacks %q: %s", want, line)
		}
	}
	if strings.Contains(e.logs.String(), "екретн") {
		t.Fatal("message text in the logs")
	}
}

func TestAgentTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, Config{}, nil)
	e.smart.push(fail(provider.KindRateLimit))
	e.cheap.push(say("секретный ответ"))
	e.turn(t, message("telegram:user:42", "Igor", "секретный вопрос"))
	e.now = e.now.Add(2 * time.Minute) // past the rate-limit cooldown
	e.turn(t, message("telegram:user:42", "Igor", "ещё"))

	for _, c := range []struct {
		name  string
		attrs map[string]string
		want  float64
	}{
		{"ganglion.turns", map[string]string{"outcome": "ok"}, 2},
		{"ganglion.turn.duration", map[string]string{"outcome": "ok"}, 2},
		{"ganglion.model.calls", map[string]string{"provider": "p1", "model": "smart", "result": "rate_limit"}, 1},
		{"ganglion.model.calls", map[string]string{"provider": "p2", "model": "cheap", "result": "ok"}, 1},
		{"ganglion.model.calls", map[string]string{"provider": "p1", "model": "smart", "result": "ok"}, 1},
		{"ganglion.model.call.duration", map[string]string{"provider": "p2", "model": "cheap"}, 1},
		{"ganglion.model.tokens", map[string]string{"provider": "p2", "kind": "input"}, 100},
		{"ganglion.model.tokens", map[string]string{"provider": "p2", "kind": "output"}, 10},
	} {
		if got := rec.Value(t, c.name, c.attrs); got != c.want {
			t.Errorf("%s %v = %v, want %v", c.name, c.attrs, got, c.want)
		}
	}
	if got := rec.Value(t, "ganglion.model.cost_usd", map[string]string{"provider": "p1"}); got < 0.000119 || got > 0.000121 {
		t.Errorf("cost %v, want 0.00012", got)
	}
	calls := 0
	for _, s := range rec.Spans.Ended() {
		if s.Name() == "model.call" {
			calls++
		}
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), "екретн") {
				t.Fatalf("span %s carries message text in %s", s.Name(), a.Key)
			}
		}
	}
	if calls != 3 {
		t.Fatalf("%d model.call spans, want 3", calls)
	}
}
