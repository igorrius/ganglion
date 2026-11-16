package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// hookTool is a tool that runs during while it runs: an addition can arrive
// then.
type hookTool struct {
	fakeTool
	during func()
}

func newHookTool(during func()) *hookTool {
	return &hookTool{result: tool.Result{Text: "12:00"}, during: during}
}

func (h *hookTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if h.during != nil {
		h.during()
		h.during = nil
	}
	return h.fakeTool.Run(ctx, c)
}

// addEnv is an env with the real queue, in the database the env has.
type addEnv struct {
	*env
	q *sqlqueue.Queue
}

// onQueue moves e onto the real queue and rebuilds its agent with cfg.
func onQueue(t *testing.T, e *env, cfg Config) *addEnv {
	t.Helper()
	q, err := sqlqueue.New(context.Background(), e.db, sqlqueue.WithClock(func() time.Time { return e.now }))
	if err != nil {
		t.Fatal(err)
	}
	e.deps.Inbox, e.deps.Outbox, e.deps.States = q, q, q
	e.a = New(overDefaults(cfg), e.deps)
	return &addEnv{env: e, q: q}
}

func newAddEnv(t *testing.T, cfg Config, hs ...tool.Handler) *addEnv {
	t.Helper()
	return onQueue(t, newEnv(t, cfg, registry(t, hs...)), cfg)
}

func (e *addEnv) put(t *testing.T, evs ...event.Event) {
	t.Helper()
	for _, ev := range evs {
		if err := e.q.Put(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
}

// next runs the next event's turn and completes it, as the core's loop does.
func (e *addEnv) next(t *testing.T) ([]event.Outgoing, queue.StateUpdate) {
	t.Helper()
	ctx := context.Background()
	l, err := e.q.Lease(ctx, time.Minute)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	out, st, err := e.a.Handle(ctx, l.Event)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := e.q.Complete(ctx, l, out, st); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	e.states.apply(st)
	return out, st
}

// leased reports whether an event is ready, and its id.
func (e *addEnv) leased(t *testing.T) string {
	t.Helper()
	l, err := e.q.Lease(context.Background(), time.Minute)
	if errors.Is(err, queue.ErrEmpty) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return l.Event.ID
}

// outbox is every stored message, oldest first.
func (e *addEnv) outbox(t *testing.T) []event.Outgoing {
	t.Helper()
	rows, err := e.db.QueryContext(context.Background(), `SELECT body FROM outbox ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []event.Outgoing
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		var m event.Outgoing
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func from(text string) event.Event { return message(owner, "Igor", text) }

func withRef(ev event.Event, ref string) event.Event {
	var m event.Message
	if err := json.Unmarshal(ev.Payload, &m); err != nil {
		panic(err)
	}
	m.Ref = ref
	ev.Payload, _ = json.Marshal(m)
	return ev
}

// sayAfter is a model step that runs before and then answers text.
func sayAfter(before func(), text string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		before()
		return answerText(text), nil
	}
}

// callsAfter is a model step that runs before, writes text and asks for the
// named tools in one message.
func callsAfter(before func(), text string, names ...string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		before()
		msg := provider.Message{Role: "assistant", Content: text}
		for i, n := range names {
			msg.ToolCalls = append(msg.ToolCalls, provider.ToolCall{ID: fmt.Sprintf("call_%d_%d", len(req.Messages), i), Name: n, Args: json.RawMessage(`{}`)})
		}
		return provider.ChatResponse{Message: msg, Usage: provider.Usage{InputTokens: 50, OutputTokens: 5}}, nil
	}
}

func contents(msgs []provider.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func toolMsgs(msgs []provider.Message) []provider.Message {
	var out []provider.Message
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

func TestAdditionJoinsBeforeTheNextModelCall(t *testing.T) {
	var e *addEnv
	add := withRef(from("also check logs"), "77")
	clock := newHookTool(func() { e.put(t, add) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(callTool("clock"), say("answer"))
	e.put(t, withRef(from("check the disk"), "76"))

	out, st := e.next(t)

	reqs := e.smart.requests()
	if len(reqs) != 2 || lastUser(reqs[1]) != "Igor: also check logs" {
		t.Fatalf("the second request's last user message is %q", lastUser(reqs[1]))
	}
	if len(out) != 1 || out[0].Text != "answer" || out[0].ReplyTo != "77" {
		t.Fatalf("out %+v", out)
	}
	if !slices.Equal(st.Taken, []string{add.ID}) {
		t.Fatalf("Taken %v, want %s", st.Taken, add.ID)
	}
	if id := e.leased(t); id != "" {
		t.Fatalf("%s was left to run as its own turn", id)
	}
	got := e.state(t).Messages
	if len(got) != 5 || got[0].Content != "Igor: check the disk" || got[3].Content != "Igor: also check logs" || got[4].Content != "answer" {
		t.Fatalf("history %q", contents(got))
	}
}

// A message that arrived before the first model call joins that call.
func TestAdditionAlreadyWaitingJoinsTheFirstCall(t *testing.T) {
	e := newAddEnv(t, Config{})
	add := from("second")
	e.put(t, from("first"), add)
	e.smart.push(say("answer"))
	_, st := e.next(t)
	if !slices.Equal(st.Taken, []string{add.ID}) {
		t.Fatalf("Taken %v", st.Taken)
	}
	reqs := e.smart.requests()
	if len(reqs) != 1 || lastUser(reqs[0]) != "Igor: second" {
		t.Fatalf("%d requests, last user %q", len(reqs), lastUser(reqs[0]))
	}
}

func TestWaitingAdditionStopsTheFirstToolCall(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{}, clock, &fakeTool{name: "other", result: tool.Result{Text: "x"}})
	e.smart.push(callsAfter(func() { e.put(t, from("don't touch it")) }, "", "clock", "other"), say("ok"))
	first := from("do both")
	e.put(t, first)

	e.next(t)

	if len(clock.calls) != 0 {
		t.Fatalf("a tool ran with an addition waiting: %d calls", len(clock.calls))
	}
	msgs := e.smart.requests()[1].Messages
	results := toolMsgs(msgs)
	if len(results) != 2 || results[0].Content != skippedResult || results[1].Content != skippedResult {
		t.Fatalf("results %q", contents(results))
	}
	if results[0].ToolCallID == results[1].ToolCallID || results[0].ToolCallID == "" {
		t.Fatalf("results not paired: %+v", results)
	}
	if lastUser(e.smart.requests()[1]) != "Igor: don't touch it" {
		t.Fatalf("last user %q", lastUser(e.smart.requests()[1]))
	}
	tr, err := e.turns.Get(context.Background(), first.ID)
	if err != nil || len(tr.Tools) != 0 {
		t.Fatalf("a skipped call was traced: %+v, %v", tr.Tools, err)
	}
}

func TestAdditionSkipsTheRestOfABatch(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("wait")) })
	other := &fakeTool{name: "other", result: tool.Result{Text: "x"}}
	e = newAddEnv(t, Config{}, clock, other)
	e.smart.push(callsAfter(func() {}, "", "clock", "other", "other"), say("ok"))
	e.put(t, from("do three"))

	e.next(t)

	if len(clock.calls) != 1 || len(other.calls) != 0 {
		t.Fatalf("calls: clock %d, other %d", len(clock.calls), len(other.calls))
	}
	results := toolMsgs(e.smart.requests()[1].Messages)
	if want := []string{"12:00", skippedResult, skippedResult}; !slices.Equal(contents(results), want) {
		t.Fatalf("results %q", contents(results))
	}
}

func TestEarlierAnswerGoesOutOnceAsAQuoteAndStaysInHistory(t *testing.T) {
	e := newAddEnv(t, Config{})
	first := from("question")
	add := from("addition")
	e.smart.push(sayAfter(func() { e.put(t, add) }, "first"), say("second"))
	e.put(t, first)

	out, _ := e.next(t)

	if len(out) != 1 || out[0].Text != "second" || out[0].Superseded {
		t.Fatalf("reply %+v", out)
	}
	box := e.outbox(t)
	if len(box) != 2 || box[0].IdemKey != first.IdemKey+":earlier:0" || box[0].Text != "first" || !box[0].Superseded || box[1].Text != "second" {
		t.Fatalf("outbox %+v", box)
	}
	want := []string{"Igor: question", "first", "Igor: addition", "second"}
	if got := contents(e.state(t).Messages); !slices.Equal(got, want) {
		t.Fatalf("history %q", got)
	}
}

func TestTextBesideSkippedCallsGoesOutAsAQuote(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	first := from("question")
	e.smart.push(callsAfter(func() { e.put(t, from("addition")) }, "checking", "clock"), say("done"))
	e.put(t, first)

	e.next(t)

	box := e.outbox(t)
	if len(box) != 2 || box[0].Text != "checking" || !box[0].Superseded || box[0].IdemKey != first.IdemKey+":earlier:0" {
		t.Fatalf("outbox %+v", box)
	}
}

func TestTextBesideACallThatRanIsNotAQuote(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	e.smart.push(callsAfter(func() {}, "checking", "clock"), say("done"))
	e.put(t, from("question"))

	e.next(t)

	if box := e.outbox(t); len(box) != 1 || box[0].Text != "done" {
		t.Fatalf("outbox %+v", box)
	}
}

func TestRerunDoesNotSendTheQuoteTwice(t *testing.T) {
	e := newAddEnv(t, Config{})
	first := from("question")
	e.put(t, first)
	ctx := context.Background()
	for i := range 2 {
		// On the second run the first addition joins at once, so a new one
		// has to arrive for the answer to be overtaken again.
		e.smart.push(sayAfter(func() { e.put(t, from(fmt.Sprintf("addition %d", i))) }, "first"), say("second"))
		if _, _, err := e.a.Handle(ctx, first); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, m := range e.outbox(t) {
		if m.IdemKey == first.IdemKey+":earlier:0" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d quotes under the key", n)
	}
}

// Review focus 1: taking stops at the first event that does not qualify, so
// nothing overtakes it.
func TestTakingStopsAtTheFirstThatDoesNotQualify(t *testing.T) {
	ok := func() event.Event { return from("ok") }
	expired := func() event.Event {
		ev := from("late")
		ev.TTL = time.Minute
		ev.At = t0.Add(-time.Hour)
		return ev
	}
	forwardedCommand := func() event.Event {
		return outsideMessage(event.Message{Text: "/new", From: owner, Name: "Igor", Forwarded: true})
	}
	for _, tc := range []struct {
		name   string
		behind []func() event.Event
		taken  int
	}{
		{"a command", []func() event.Event{ok, func() event.Event { return from("/new") }, ok}, 1},
		{"a command with the bot's name", []func() event.Event{func() event.Event { return from("/NEW@bot") }, ok}, 0},
		{"another person", []func() event.Event{func() event.Event { return message("telegram:user:7", "Ann", "hi") }, ok}, 0},
		{"a press", []func() event.Event{func() event.Event { return press(owner, "b1", "1") }, ok}, 0},
		{"an expired message", []func() event.Event{expired, ok}, 0},
		{"an empty message", []func() event.Event{func() event.Event { return from("   ") }, ok}, 0},
		{"a forward of a command is words, not a command", []func() event.Event{forwardedCommand, ok}, 2},
		{"an unknown command is words", []func() event.Event{func() event.Event { return from("/nope") }}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddEnv(t, Config{})
			e.put(t, from("first"))
			var behind []event.Event
			for _, mk := range tc.behind {
				ev := mk()
				behind = append(behind, ev)
				e.put(t, ev)
			}
			e.smart.push(say("answer"))
			_, st := e.next(t)
			var want []string
			for _, ev := range behind[:tc.taken] {
				want = append(want, ev.ID)
			}
			if !slices.Equal(st.Taken, want) {
				t.Fatalf("Taken %v, want %v", st.Taken, want)
			}
		})
	}
}

// Review focus 1: "also check logs", then /new. The addition joins, /new
// waits and archives a session with the finished answer in it.
func TestCommandBehindAnAdditionArchivesTheAnswer(t *testing.T) {
	e := newAddEnv(t, Config{})
	e.put(t, from("check the disk"), from("also check logs"), from("/new"))
	e.smart.push(say("all fine"))
	e.next(t)
	e.next(t)
	ctx := context.Background()
	list, err := e.archive.List(ctx, "telegram:42", 5, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("archive %+v, %v", list, err)
	}
	got, err := e.archive.Get(ctx, "telegram:42", list[0].Session)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Igor: check the disk", "Igor: also check logs", "all fine"}
	if !slices.Equal(contents(got.Messages), want) {
		t.Fatalf("archived %q", contents(got.Messages))
	}
	if id := e.leased(t); id != "" {
		t.Fatalf("%s is still waiting", id)
	}
}

func TestAdditionQualifiesOnlyInAPersonsTurn(t *testing.T) {
	e := newAddEnv(t, Config{})
	head := from("first")
	add := from("second")
	e.put(t, head, add)
	var msg event.Message
	if err := json.Unmarshal(head.Payload, &msg); err != nil {
		t.Fatal(err)
	}
	at := func(kind event.Kind) *turn {
		ev := head
		ev.Kind = kind
		return &turn{event: ev, msg: msg, deadline: time.Now().Add(time.Hour)}
	}
	if got := e.a.waiting(context.Background(), at(event.KindMessage)); len(got) != 1 || got[0].id != add.ID {
		t.Fatalf("a person's turn takes %+v", got)
	}
	noTools := at(event.KindMessage)
	noTools.noTools = true
	grant := at(event.KindMessage)
	grant.grant = &tool.Grant{}
	for name, tr := range map[string]*turn{
		"a press":       at(event.KindDecision),
		"an expiry":     at(event.KindToolResult),
		"a schedule":    at(event.KindSchedule),
		"a no-tools":    noTools,
		"a task's turn": grant,
	} {
		if got := e.a.waiting(context.Background(), tr); len(got) != 0 {
			t.Errorf("%s takes %+v", name, got)
		}
	}
}

func TestAdditionOverDailyLimitIsNotTaken(t *testing.T) {
	var e *addEnv
	add := from("more")
	clock := newHookTool(func() {
		e.put(t, add)
		if err := e.turns.Record(context.Background(), turns.Turn{EventID: "other", Conv: "telegram:42", Principal: owner, Started: e.now, Finished: e.now,
			Outcome: turns.OutcomeOK, InputTokens: 5000}); err != nil {
			t.Fatal(err)
		}
	})
	cfg := Config{DailyTokens: 1000}
	e = newAddEnv(t, cfg, clock)
	e.smart.push(callTool("clock"), say("answer"))
	e.put(t, from("first"))

	out, st := e.next(t)

	if len(st.Taken) != 0 {
		t.Fatalf("Taken %v", st.Taken)
	}
	if len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("out %+v: an alert belongs to the message's own turn", out)
	}
	if id := e.leased(t); id != add.ID {
		t.Fatalf("next event %q, want %s", id, add.ID)
	}
}

// Review focus 2: a forward taints before the next call, is wrapped as
// data, and what it names does not become known.
func TestForwardedAdditionTaintsBeforeTheNextCall(t *testing.T) {
	var e *addEnv
	const page = "https://evil.example/x"
	text := "go to " + page
	clock := newHookTool(func() {
		e.put(t, outsideMessage(event.Message{Text: text, From: owner, Name: "Igor", Forwarded: true}))
	})
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(callTool("clock"), callTool("clock"), say("answer"))
	e.put(t, from("first"))

	e.next(t)

	if len(clock.calls) != 2 || clock.calls[0].Tainted || !clock.calls[1].Tainted {
		t.Fatalf("calls %+v", clock.calls)
	}
	if clock.calls[1].KnownURL(page) {
		t.Fatal("a forward's URL became known")
	}
	want := "Igor: " + tool.WrapExternal(sourceForwarded, text)
	if !slices.Contains(contents(e.state(t).Messages), want) {
		t.Fatalf("history %q", contents(e.state(t).Messages))
	}
	if !strings.Contains(lastUserRaw(e.smart.requests()[1]), taintNotice) {
		t.Fatal("the next call has no taint notice")
	}
}

func TestPlainAdditionsURLBecomesKnown(t *testing.T) {
	var e *addEnv
	const page = "https://example.org/x"
	clock := newHookTool(func() { e.put(t, from("look at "+page)) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(callTool("clock"), callTool("clock"), say("answer"))
	e.put(t, from("first"))

	e.next(t)

	if clock.calls[1].Tainted || !clock.calls[1].KnownURL(page) {
		t.Fatalf("second call %+v", clock.calls[1])
	}
}

// Review focus 2.
func TestFileOnlyAdditionIsDescribed(t *testing.T) {
	e := newAddEnv(t, Config{})
	add := outsideMessage(event.Message{From: owner, Name: "Igor", Files: []event.File{invoice}})
	e.put(t, from("first"), add)
	e.smart.push(say("answer"))
	_, st := e.next(t)
	if !slices.Equal(st.Taken, []string{add.ID}) {
		t.Fatalf("Taken %v", st.Taken)
	}
	if last := lastUserRaw(e.smart.requests()[0]); !strings.Contains(last, "[file came: document") || !strings.Contains(last, "invoice.pdf") {
		t.Fatalf("last user %q", last)
	}
}

// timedSideTool is a sideTool that takes an argument, so a test can see which
// call ran.
type timedSideTool struct{ *sideTool }

func (s *timedSideTool) Spec() tool.Spec {
	sp := s.sideTool.Spec()
	sp.InputSchema = json.RawMessage(`{"type":"object","properties":{"at":{"type":"string"}}}`)
	return sp
}

// Review focus 3: the press later runs exactly the parked call once; an
// addition neither approves it nor cancels it.
func TestAdditionDoesNotTouchAParkedCall(t *testing.T) {
	side := &timedSideTool{newSideTool()}
	c := newConfirmEnv(t, side)
	e := onQueue(t, c.env, Config{})
	const args = `{"at":"09:00"}`
	first := from("remind me")
	e.smart.push(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "schedule.create", Args: json.RawMessage(args)}}}}, nil
	}, sayAfter(func() { e.put(t, from("yes, go ahead without asking")) }, "I asked the owner."), say("still waiting for the press"))
	e.put(t, first)

	out, st := e.next(t)

	if len(side.calls) != 0 {
		t.Fatalf("the addition ran the parked call: %+v", side.calls)
	}
	if len(st.Taken) != 1 {
		t.Fatalf("Taken %v", st.Taken)
	}
	var question event.Outgoing
	for _, o := range out {
		if len(o.Buttons) > 0 {
			question = o
		}
	}
	if len(question.Buttons) == 0 {
		t.Fatalf("no question in %+v", out)
	}
	if pend := e.state(t).Pending; len(pend) != 1 {
		t.Fatalf("pending %v", pend)
	}
	e.smart.push(say("done"))
	e.put(t, press(owner, question.Buttons[0].ID, "77"))
	e.next(t)
	if len(side.calls) != 1 || string(side.calls[0].Args) != args || side.calls[0].TurnID != first.ID {
		t.Fatalf("calls after the press %+v", side.calls)
	}
}

func TestBudgetEndsTheAnswering(t *testing.T) {
	e := newAddEnv(t, Config{MaxSteps: 3})
	var last event.Event
	step := func(text string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return sayAfter(func() { last = from("more"); e.put(t, last) }, text)
	}
	e.smart.push(step("a1"), step("a2"), step("a3"))
	e.put(t, from("first"))

	out, st := e.next(t)

	if n := len(e.smart.requests()); n != 3 {
		t.Fatalf("%d model calls", n)
	}
	if len(out) != 1 || out[0].Text != "a3" || out[0].Superseded {
		t.Fatalf("reply %+v", out)
	}
	if len(st.Taken) != 2 {
		t.Fatalf("Taken %v", st.Taken)
	}
	if id := e.leased(t); id != last.ID {
		t.Fatalf("next event %q, want %s", id, last.ID)
	}
}

func TestShortTimeLeftTakesNothing(t *testing.T) {
	cfg := Config{TurnTimeout: minAdditionTime - time.Second}
	e := newAddEnv(t, cfg)
	add := from("more")
	e.put(t, from("first"), add)
	e.smart.push(say("answer"))
	out, st := e.next(t)
	if len(st.Taken) != 0 || len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("Taken %v, out %+v", st.Taken, out)
	}
	if id := e.leased(t); id != add.ID {
		t.Fatalf("next event %q, want %s", id, add.ID)
	}
}

func TestFailedTurnKeepsTakenMessages(t *testing.T) {
	var e *addEnv
	add := from("more")
	clock := newHookTool(func() { e.put(t, add) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(callTool("clock"), fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	e.put(t, from("first"))

	out, st := e.next(t)

	if len(out) != 1 || out[0].Text != replyNoModel {
		t.Fatalf("out %+v", out)
	}
	if got, want := contents(e.state(t).Messages), []string{"Igor: first", "Igor: more"}; !slices.Equal(got, want) {
		t.Fatalf("history %q", got)
	}
	if !slices.Equal(st.Taken, []string{add.ID}) {
		t.Fatalf("Taken %v", st.Taken)
	}
	if id := e.leased(t); id != "" {
		t.Fatalf("%s runs again", id)
	}
}

// R-LIVE-5: the person read the earlier answer, so a failure after it does
// not take it out of the history.
func TestFailedTurnKeepsTheEarlierAnswer(t *testing.T) {
	e := newAddEnv(t, Config{})
	e.smart.push(sayAfter(func() { e.put(t, from("more")) }, "earlier"), fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	e.put(t, from("first"))
	e.next(t)
	if got, want := contents(e.state(t).Messages), []string{"Igor: first", "earlier", "Igor: more"}; !slices.Equal(got, want) {
		t.Fatalf("history %q", got)
	}
}

type brokenInbox struct{}

func (brokenInbox) Behind(context.Context, event.Event, int) ([]event.Event, error) {
	return nil, errors.New("disk on fire")
}

func TestInboxReadFailureDoesNotFailTheTurn(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.Inbox = brokenInbox{}
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(say("answer"))
	out := e.turn(t, from("first"))
	if len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("out %+v", out)
	}
	if !strings.Contains(e.logs.String(), "agent: inbox not read") {
		t.Fatalf("logs %s", e.logs)
	}
}

// A quote that cannot be stored goes out with the turn: nothing is lost.
func TestQuoteThatCannotBeStoredGoesWithTheTurn(t *testing.T) {
	e := newAddEnv(t, Config{})
	e.deps.Outbox = &enqueued{err: errors.New("disk on fire")}
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(sayAfter(func() { e.put(t, from("more")) }, "first"), say("second"))
	e.put(t, from("question"))
	out, _ := e.next(t)
	if len(out) != 2 || out[0].Text != "first" || !out[0].Superseded || out[1].Text != "second" {
		t.Fatalf("out %+v", out)
	}
}

// A skill.read that was skipped was not read, so it does not route the
// turn, and a session is not made local-only by it.
func TestSkippedSkillReadDoesNotRouteTheTurn(t *testing.T) {
	c, _ := localEnv(t, Config{})
	e := onQueue(t, c.env, Config{})
	e.smart.push(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		e.put(t, from("more"))
		return provider.ChatResponse{Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "skill.read", Args: json.RawMessage(`{"name":"private"}`)}}}}, nil
	}, say("answer"))
	e.put(t, from("first"))
	e.next(t)
	if e.state(t).LocalOnly {
		t.Fatal("a skipped read made the session local-only")
	}
}

func TestAdditionMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	e.smart.push(callsAfter(func() { e.put(t, from("a")) }, "checking", "clock"), sayAfter(func() { e.put(t, from("b")) }, "first"), say("second"))
	e.put(t, from("question"))
	e.next(t)
	for name, want := range map[string]float64{
		"ganglion.additions.taken":         2,
		"ganglion.additions.skipped_calls": 1,
		"ganglion.additions.quotes":        2,
	} {
		if got := rec.Value(t, name, nil); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if !strings.Contains(e.logs.String(), "additions=2") {
		t.Errorf("the turn's log line has no count: %s", e.logs)
	}
}

// execThenBlock is a model step that behaves like a CLI run: it runs the
// calls through Exec and then keeps working until its context ends, reporting
// the cause it saw.
func execThenBlock(cause *error, names ...string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		for _, n := range names {
			_, _ = req.Exec(ctx, provider.ToolCall{Name: n, Args: json.RawMessage(`{}`)})
		}
		<-ctx.Done()
		if cause != nil {
			*cause = context.Cause(ctx)
		}
		return provider.ChatResponse{}, fmt.Errorf("provider x: %w", context.Canceled)
	}
}

func TestCLIRunEndsAfterAToolWhenAnAdditionWaits(t *testing.T) {
	var e *addEnv
	add := from("also this")
	clock := newHookTool(func() { e.put(t, add) })
	e = newAddEnv(t, Config{}, clock)
	var cause error
	e.smart.push(execThenBlock(&cause, "clock"), say("answer"))
	first := from("first")
	e.put(t, first)

	out, st := e.next(t)

	if !errors.Is(cause, errAdditionWaiting) {
		t.Fatalf("the run ended with cause %v", cause)
	}
	if len(out) != 1 || out[0].Text != "answer" || !slices.Equal(st.Taken, []string{add.ID}) {
		t.Fatalf("out %+v, Taken %v", out, st.Taken)
	}
	msgs := e.smart.requests()[1].Messages
	n := len(msgs)
	if n < 3 || len(msgs[n-3].ToolCalls) != 1 || msgs[n-3].ToolCalls[0].Name != "clock" || msgs[n-2].Content != "12:00" ||
		!strings.HasPrefix(msgs[n-1].Content, "Igor: also this") {
		t.Fatalf("second request %q", contents(msgs))
	}
	if tr, err := e.turns.Get(context.Background(), first.ID); err != nil || tr.Outcome != turns.OutcomeOK {
		t.Fatalf("turn %+v, %v", tr, err)
	}
}

func TestCLICallIsRefusedWhenAnAdditionAlreadyWaits(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	var refused error
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		e.put(t, from("don't"))
		_, refused = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}, execAndAnswer("clock", "answer"))
	first := from("first")
	e.put(t, first)

	e.next(t)

	if !errors.Is(refused, errAdditionWaiting) {
		t.Fatalf("Exec returned %v", refused)
	}
	if len(clock.calls) != 1 || clock.calls[0].N != 1 {
		t.Fatalf("the refused call was numbered: %+v", clock.calls)
	}
	tr, err := e.turns.Get(context.Background(), first.ID)
	if err != nil || len(tr.Tools) != 1 {
		t.Fatalf("the refused call was traced: %+v, %v", tr.Tools, err)
	}
	for _, m := range e.state(t).Messages {
		if strings.Contains(m.Content, errAdditionWaiting.Error()) {
			t.Fatalf("the refusal is in the history: %q", m.Content)
		}
	}
}

// execAndAnswer is a CLI run that runs a tool and then answers.
func execAndAnswer(name, answer string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		if _, err := req.Exec(ctx, provider.ToolCall{Name: name, Args: json.RawMessage(`{}`)}); err != nil {
			return provider.ChatResponse{}, err
		}
		return answerText(answer), nil
	}
}

// Calls of one run may come in parallel: one the run's end overtook does not
// start, whatever a read of the inbox would say.
func TestCLICallAfterTheRunEndedIsRefused(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	var second error
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		_, _ = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		_, second = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		return provider.ChatResponse{}, ctx.Err()
	}, say("answer"))
	e.put(t, from("first"))
	e.next(t)
	if !errors.Is(second, errAdditionWaiting) || len(clock.calls) != 1 {
		t.Fatalf("second call: %v, %d ran", second, len(clock.calls))
	}
}

func TestEndedRunIsNotAFailure(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(execThenBlock(nil, "clock"), say("answer"))
	first := from("first")
	e.put(t, first)

	out, _ := e.next(t)

	if n := len(e.cheap.requests()); n != 0 {
		t.Fatalf("a fallback model was tried %d times", n)
	}
	if len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("out %+v", out)
	}
	tr, err := e.turns.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Error != "" || tr.Outcome != turns.OutcomeOK || len(tr.Route) != 2 || tr.Route[0].Error != "addition" || tr.Route[1].Error != "" {
		t.Fatalf("turn %+v", tr)
	}
	if strings.Contains(e.logs.String(), "model call failed") || strings.Contains(e.logs.String(), "no fallback") {
		t.Fatalf("logged as a failure: %s", e.logs)
	}
}

// An ended run reports no usage, so the daily limit counts what was sent, once
// for each model call the run made.
func TestEndedRunTokensAreEstimatedIntoTheTurn(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(execThenBlock(nil, "clock"), say("answer"))
	first := from("first")
	e.put(t, first)

	e.next(t)

	est := estimateTokens(e.smart.requests()[0].Messages)
	if est == 0 {
		t.Fatal("no estimate")
	}
	tr, err := e.turns.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The second run's answer reports 100; two model calls in the ended one:
	// the one that asked for the tool and the one the end cut.
	if want := 100 + 2*est; tr.InputTokens != want {
		t.Fatalf("input tokens %d, want %d", tr.InputTokens, want)
	}
}

func TestAnswerRacingTheEndIsKept(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		_, _ = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		return answerText("racing"), nil
	}, say("second"))
	e.put(t, from("first"))

	out, _ := e.next(t)

	box := e.outbox(t)
	if len(out) != 1 || out[0].Text != "second" || len(box) != 2 || box[0].Text != "racing" || !box[0].Superseded {
		t.Fatalf("out %+v, outbox %+v", out, box)
	}
}

// With no step left for a new run, ending this one would lose its answer.
func TestLastStepDoesNotEndTheRun(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	cfg := Config{MaxSteps: 1}
	e = newAddEnv(t, cfg, clock)
	e.smart.push(execAndAnswer("clock", "answer"))
	e.put(t, from("first"))
	out, st := e.next(t)
	if len(out) != 1 || out[0].Text != "answer" || len(st.Taken) != 0 {
		t.Fatalf("out %+v, Taken %v", out, st.Taken)
	}
}

func TestEndedRunMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(execThenBlock(nil, "clock"), say("answer"))
	e.put(t, from("first"))
	e.next(t)
	if got := rec.Value(t, "ganglion.additions.runs_ended", nil); got != 1 {
		t.Fatalf("runs ended = %v", got)
	}
	if got := rec.Value(t, "ganglion.model.tokens", map[string]string{"kind": "input"}); got <= 100 {
		t.Fatalf("input tokens = %v: the estimate is missing", got)
	}
	if got := rec.Value(t, "ganglion.model.calls", map[string]string{"result": "addition"}); got != 1 {
		t.Fatalf("calls ended = %v", got)
	}
}

// Review: an answer that went out as a quote is not the reply to what came
// after it.
func TestStepLimitDoesNotRepeatAQuote(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{MaxSteps: 2}, clock)
	e.smart.push(sayAfter(func() { e.put(t, from("more")) }, "earlier"), callTool("clock"))
	e.put(t, from("first"))
	out, _ := e.next(t)
	if len(out) != 1 || out[0].Text != replyStepLimit {
		t.Fatalf("out %+v", out)
	}
}

// Review: R-LIVE-2 holds when the addition cannot be taken too. It may say
// "don't", and the model would not read this call's result anyway.
func TestLastStepHoldsBackAToolCall(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{MaxSteps: 1}, clock)
	add := from("STOP, do not do it")
	e.smart.push(callsAfter(func() { e.put(t, add) }, "", "clock"))
	e.put(t, from("first"))
	_, st := e.next(t)
	if len(clock.calls) != 0 {
		t.Fatalf("the tool ran with a message waiting: %+v", clock.calls)
	}
	if id := e.leased(t); id != add.ID || len(st.Taken) != 0 {
		t.Fatalf("next %q, Taken %v", id, st.Taken)
	}
}

func TestShortTimeHoldsBackAToolCall(t *testing.T) {
	clock := newClock()
	cfg := Config{TurnTimeout: minAdditionTime - time.Second}
	e := newAddEnv(t, cfg, clock)
	add := from("STOP")
	e.smart.push(callsAfter(func() { e.put(t, add) }, "", "clock"), say("answer"))
	e.put(t, from("first"))
	out, st := e.next(t)
	if len(clock.calls) != 0 || len(st.Taken) != 0 || len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("calls %d, Taken %v, out %+v", len(clock.calls), st.Taken, out)
	}
	if id := e.leased(t); id != add.ID {
		t.Fatalf("next %q", id)
	}
}

// With no step for a new run, a later call of the run is refused and the run
// goes on, so its answer survives.
func TestLastStepRefusesALaterCallOfTheRun(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{MaxSteps: 1}, clock)
	var second, alive error
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		_, _ = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		_, second = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		alive = ctx.Err()
		return answerText("answer"), nil
	})
	e.put(t, from("first"))
	out, _ := e.next(t)
	if !errors.Is(second, errNoRoomForAddition) || alive != nil || len(clock.calls) != 1 {
		t.Fatalf("second %v, ctx %v, %d calls", second, alive, len(clock.calls))
	}
	if len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("out %+v", out)
	}
}

// Review: the calls an ended run ran stay on record when the next run fails, so
// the model knows of them when the person asks again.
func TestFailedTurnKeepsWhatAnEndedRunRan(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, from("more")) })
	e = newAddEnv(t, Config{}, clock)
	e.smart.push(execThenBlock(nil, "clock"), fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	e.put(t, from("first"))
	e.next(t)
	got := e.state(t).Messages
	if len(got) != 4 || got[1].ToolCalls[0].Name != "clock" || got[2].Content != "12:00" || got[3].Content != "Igor: more" {
		t.Fatalf("history %+v", got)
	}
}

func TestBlankTextBesideSkippedCallsIsNotAQuote(t *testing.T) {
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	e.smart.push(callsAfter(func() { e.put(t, from("more")) }, "\n\n", "clock"), say("done"))
	e.put(t, from("first"))
	e.next(t)
	if box := e.outbox(t); len(box) != 1 || box[0].Text != "done" {
		t.Fatalf("outbox %+v", box)
	}
}

func TestRefusedAndFailedReadsAreCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	clock := newClock()
	e := newAddEnv(t, Config{}, clock)
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		e.put(t, from("more"))
		_, _ = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		_, _ = req.Exec(ctx, provider.ToolCall{Name: "clock", Args: json.RawMessage(`{}`)})
		return provider.ChatResponse{}, ctx.Err()
	}, say("answer"))
	e.put(t, from("first"))
	e.next(t)
	if got := rec.Value(t, "ganglion.additions.skipped_calls", nil); got != 2 {
		t.Fatalf("skipped = %v", got)
	}
	b := newEnv(t, Config{}, nil)
	b.deps.Inbox = brokenInbox{}
	b.a = New(overDefaults(Config{}), b.deps)
	b.turn(t, from("x"))
	if got := rec.Value(t, "ganglion.additions.inbox_errors", nil); got == 0 {
		t.Fatal("a failed read was not counted")
	}
}

// recordStatus makes the agent report its stages to a list.
func recordStatus(t *testing.T, e *addEnv, cfg Config) *[]event.Status {
	t.Helper()
	var got []event.Status
	e.deps.Status = func(st event.Status) { got = append(got, st) }
	e.a = New(overDefaults(cfg), e.deps)
	return &got
}

// R-LIVE-7: the first message shows the work; each taken one its mark; all end
// on every path.
func TestStagesAreReported(t *testing.T) {
	var e *addEnv
	clock := newHookTool(func() { e.put(t, withRef(from("more"), "77")) })
	e = newAddEnv(t, Config{}, clock)
	got := recordStatus(t, e, Config{})
	e.smart.push(callTool("clock"), say("answer"))
	e.put(t, withRef(from("first"), "76"))
	e.next(t)
	want := []event.Status{
		{Transport: "telegram", Conv: "telegram:42", Ref: "76", Stage: event.StageThinking},
		{Transport: "telegram", Conv: "telegram:42", Ref: "76", Stage: event.StageTool},
		{Transport: "telegram", Conv: "telegram:42", Ref: "77", Stage: event.StageAdded},
		{Transport: "telegram", Conv: "telegram:42", Ref: "76", Stage: event.StageDone},
		{Transport: "telegram", Conv: "telegram:42", Ref: "77", Stage: event.StageDone},
	}
	if !slices.Equal(*got, want) {
		t.Fatalf("stages %+v", *got)
	}
}

func TestCommandAndFailureEndTheReaction(t *testing.T) {
	e := newAddEnv(t, Config{})
	got := recordStatus(t, e, Config{})
	e.put(t, withRef(from("/sessions"), "5"))
	e.next(t)
	e.smart.push(fail(provider.KindUnavailable))
	e.cheap.push(fail(provider.KindUnavailable))
	e.put(t, withRef(from("x"), "6"))
	e.next(t)
	var done []string
	for _, st := range *got {
		if st.Stage == event.StageDone {
			done = append(done, st.Ref)
		}
	}
	if !slices.Equal(done, []string{"5", "6"}) {
		t.Fatalf("done %v", done)
	}
}

// A message with nothing to answer has no turn, and its reaction still ends.
func TestUnanswerableMessageEndsItsReaction(t *testing.T) {
	e := newAddEnv(t, Config{})
	got := recordStatus(t, e, Config{})
	e.put(t, withRef(from("   "), "5"))
	e.next(t)
	if len(*got) != 1 || (*got)[0].Stage != event.StageDone || (*got)[0].Ref != "5" {
		t.Fatalf("stages %+v", *got)
	}
}

func TestPressReportsNoStage(t *testing.T) {
	e := newAddEnv(t, Config{})
	got := recordStatus(t, e, Config{})
	e.put(t, press(owner, "nope", "5"))
	e.next(t)
	if len(*got) != 0 {
		t.Fatalf("stages %+v", *got)
	}
}

// runAndAnswer is a CLI run that makes the calls in order and then answers.
func runAndAnswer(answer string, names ...string) func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		for _, n := range names {
			_, _ = req.Exec(ctx, provider.ToolCall{Name: n, Args: json.RawMessage(`{}`)})
		}
		return answerText(answer), nil
	}
}

// R-LIVE-8: ticks do not eat the turn's real calls.
func TestPlanCallsDoNotSpendTheCap(t *testing.T) {
	clock := newClock()
	step := &fakeTool{name: "plan.step", result: tool.Result{Text: "marked"}}
	e := newAddEnv(t, Config{}, clock, step)
	var names []string
	for range DefaultConfig().MaxSteps {
		names = append(names, "plan.step", "clock", "plan.step")
	}
	e.smart.push(runAndAnswer("answer", names...))
	e.put(t, from("first"))
	e.next(t)
	if len(clock.calls) != DefaultConfig().MaxSteps || len(step.calls) != 2*DefaultConfig().MaxSteps {
		t.Fatalf("clock %d, plan %d", len(clock.calls), len(step.calls))
	}
}

func TestPlanCallsHaveTheirOwnCeiling(t *testing.T) {
	step := &fakeTool{name: "plan.step", result: tool.Result{Text: "marked"}}
	e := newAddEnv(t, Config{}, step)
	var last error
	e.smart.push(func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		for range maxPlanCalls + 1 {
			_, last = req.Exec(ctx, provider.ToolCall{Name: "plan.step", Args: json.RawMessage(`{}`)})
		}
		return answerText("answer"), nil
	})
	e.put(t, from("first"))
	e.next(t)
	if !errors.Is(last, errPlanLimit) || len(step.calls) != maxPlanCalls {
		t.Fatalf("last %v, %d ran", last, len(step.calls))
	}
}

// On the API route one model message may hold many calls: the ceiling holds there.
func TestPlanCeilingHoldsOnTheAPIRoute(t *testing.T) {
	step := &fakeTool{name: "plan.step", result: tool.Result{Text: "marked"}}
	e := newAddEnv(t, Config{}, step)
	var names []string
	for range maxPlanCalls + 1 {
		names = append(names, "plan.step")
	}
	e.smart.push(callsAfter(func() {}, "", names...), say("answer"))
	e.put(t, from("first"))
	e.next(t)
	if len(step.calls) != maxPlanCalls {
		t.Fatalf("%d ran", len(step.calls))
	}
}

// A plan call is no work of its own, so it does not show as a tool at work.
func TestPlanCallReportsNoToolStage(t *testing.T) {
	step := &fakeTool{name: "plan.step", result: tool.Result{Text: "marked"}}
	e := newAddEnv(t, Config{}, step)
	got := recordStatus(t, e, Config{})
	e.smart.push(callTool("plan.step"), say("answer"))
	e.put(t, withRef(from("first"), "5"))
	e.next(t)
	for _, st := range *got {
		if st.Stage == event.StageTool {
			t.Fatalf("stages %+v", *got)
		}
	}
}

type planFinish struct {
	conv string
	kept []bool
	ran  []string
	err  error
}

func (p *planFinish) Ran(_ context.Context, conv string, cmd tool.Command, ok bool) error {
	p.ran = append(p.ran, fmt.Sprintf("%s %s in %s ok=%v", conv, strings.Join(cmd.Argv, " "), cmd.Dir, ok))
	return p.err
}

func (p *planFinish) Finish(_ context.Context, conv string, kept bool) error {
	p.conv, p.kept = conv, append(p.kept, kept)
	return p.err
}

func (p *planFinish) Approved(context.Context, string) ([]tool.Command, error) { return nil, p.err }

func TestTurnEndFinishesThePlan(t *testing.T) {
	e := newAddEnv(t, Config{})
	f := &planFinish{}
	e.deps.Plans = f
	e.a = New(overDefaults(Config{}), e.deps)
	e.put(t, from("first"))
	e.next(t)
	if f.conv != "telegram:42" || !slices.Equal(f.kept, []bool{false}) {
		t.Fatalf("%+v", f)
	}
}

func TestParkedCallKeepsThePlan(t *testing.T) {
	side := newSideTool()
	c := newConfirmEnv(t, side)
	f := &planFinish{}
	c.deps.Plans = f
	c.a = New(overDefaults(Config{}), c.deps)
	c.ask(t, "schedule.create")
	if !slices.Equal(f.kept, []bool{true}) {
		t.Fatalf("kept %v", f.kept)
	}
}

// R-CMD-4: a command that goes on in the background is listed once in the
// state, and the plan stays open while it runs.
func TestBackgroundCommandKeepsThePlan(t *testing.T) {
	cmd := &fakeTool{name: "host.exec", result: tool.Result{Text: "still running", External: true, Job: "k:1"}}
	e := newEnv(t, Config{}, registry(t, cmd))
	f := &planFinish{}
	e.deps.Plans = f
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(callTools("host.exec", "host.exec"), say("it runs"))
	e.turn(t, message(owner, "Igor", "clean the cache"))
	if st := e.state(t); !slices.Equal(st.Commands, []string{"k:1"}) {
		t.Fatalf("commands %v", st.Commands)
	}
	if !slices.Equal(f.kept, []bool{true}) {
		t.Fatalf("kept %v", f.kept)
	}
}

// A plan is progress: failing to close one costs the turn nothing.
func TestPlanFinishFailureDoesNotFailTheTurn(t *testing.T) {
	e := newAddEnv(t, Config{})
	e.deps.Plans = &planFinish{err: errors.New("disk on fire")}
	e.a = New(overDefaults(Config{}), e.deps)
	e.smart.push(say("answer"))
	e.put(t, from("first"))
	if out, _ := e.next(t); len(out) != 1 || out[0].Text != "answer" {
		t.Fatalf("out %+v", out)
	}
	if !strings.Contains(e.logs.String(), "agent: plan not finished") {
		t.Fatalf("logs %s", e.logs)
	}
}
