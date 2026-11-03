package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

func newTransport(t *testing.T, fake *telegramtest.Server) *Transport {
	t.Helper()
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	tr.retry.Min = 10 * time.Millisecond
	return tr
}

// collector is a sink that records events and can be told to fail.
type collector struct {
	mu     sync.Mutex
	events []event.Event
	failOn func(e event.Event, n int) bool // n counts calls from 0
	calls  int
}

func (c *collector) sink(_ context.Context, e event.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.calls
	c.calls++
	if c.failOn != nil && c.failOn(e, n) {
		return errors.New("inbox unavailable")
	}
	c.events = append(c.events, e)
	return nil
}

func (c *collector) got() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.events...)
}

// run starts tr.Run and returns a function that waits for its result.
func run(t *testing.T, tr *Transport, sink transport.Sink) (cancel func(), result func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, sink) }()
	t.Cleanup(cancel)
	return cancel, func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
			return nil
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func payload(t *testing.T, e event.Event) event.Message {
	t.Helper()
	var m event.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunPutsMessagesInOrder(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	cancel, result := run(t, newTransport(t, fake), c.sink)
	for _, text := range []string{"one", "two", "three"} {
		fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: text})
	}
	eventually(t, "three events", func() bool { return len(c.got()) == 3 })
	for i, want := range []string{"one", "two", "three"} {
		e := c.got()[i]
		m := payload(t, e)
		if m.Text != want || m.From != "telegram:user:42" || m.Name != "Igor" {
			t.Errorf("event %d payload %+v", i, m)
		}
		if e.Kind != "message" || e.Source != "telegram" || e.Conv != "telegram:42" || e.ID == "" || e.At.IsZero() {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	cancel()
	if err := result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after cancel = %v, want context.Canceled", err)
	}
}

func TestOffsetConfirmedOnlyAfterSink(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	var mu sync.Mutex
	broken := true
	c := &collector{failOn: func(e event.Event, _ int) bool {
		mu.Lock()
		defer mu.Unlock()
		return broken && payload(t, e).Text == "second"
	}}
	run(t, newTransport(t, fake), c.sink)
	id1 := fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "first"})
	id2 := fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "second"})

	eventually(t, "several failed sink calls", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls >= 4
	})
	if got := fake.Confirmed(); got >= id2 {
		t.Fatalf("confirmed %d while the sink keeps failing on %d", got, id2)
	}
	if got := len(c.got()); got != 1 {
		t.Fatalf("stored %d events, want only the first", got)
	}

	mu.Lock()
	broken = false
	mu.Unlock()
	eventually(t, "the second event", func() bool { return len(c.got()) >= 2 })
	evs := c.got()
	if payload(t, evs[1]).Text != "second" || evs[1].IdemKey != "telegram:7001:"+itoa(id2) {
		t.Fatalf("redelivered %+v", evs[1])
	}
	if payload(t, evs[0]).Text != "first" || evs[0].IdemKey != "telegram:7001:"+itoa(id1) {
		t.Fatalf("first event %+v", evs[0])
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "third"})
	eventually(t, "confirmation of the second", func() bool { return fake.Confirmed() >= id2 })
}

func TestTopicMessageGetsTopicConv(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: -1001234567890, ThreadID: 45, UserID: 1, Text: "in topic"})
	fake.AddMessage(telegramtest.Incoming{ChatID: -1001234567890, UserID: 1, Text: "general"})
	eventually(t, "two events", func() bool { return len(c.got()) == 2 })
	if got := c.got()[0].Conv; got != "telegram:-1001234567890/45" {
		t.Errorf("topic conv = %q", got)
	}
	if got := c.got()[1].Conv; got != "telegram:-1001234567890" {
		t.Errorf("general conv = %q", got)
	}
}

func TestBotAndNonTextUpdatesSkipped(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 9, IsBot: true, Text: "from a bot"})
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1}) // a sticker
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Caption: "photo caption"})
	last := fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "text"})
	eventually(t, "two events", func() bool { return len(c.got()) == 2 })
	if a, b := payload(t, c.got()[0]).Text, payload(t, c.got()[1]).Text; a != "photo caption" || b != "text" {
		t.Fatalf("events %q, %q", a, b)
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "more"})
	eventually(t, "skipped updates confirmed", func() bool { return fake.Confirmed() >= last })
}

func TestIdemKeyIncludesBotID(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	id := fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "x"})
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	if got, want := c.got()[0].IdemKey, "telegram:7001:"+itoa(id); got != want {
		t.Fatalf("IdemKey %q, want %q", got, want)
	}
}

func TestRunStopsOn409(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.SetWebhook(true)
	_, result := run(t, newTransport(t, fake), (&collector{}).sink)
	if err := result(); !errors.Is(err, ErrConflict) {
		t.Fatalf("Run = %v, want ErrConflict", err)
	}
	if n := fake.Calls("getUpdates"); n != 1 {
		t.Fatalf("getUpdates called %d times, want 1", n)
	}
}

func TestRunStopsOnWrongToken(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr, err := New("7001:wrong", WithBaseURL(fake.URL()), WithPollTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, result := run(t, tr, (&collector{}).sink)
	var ae *apiError
	if err := result(); !errors.As(err, &ae) || ae.Code != 401 {
		t.Fatalf("Run = %v, want the 401", err)
	}
}

func TestRunHonours429(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("getUpdates", 429, 1)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "x"})
	c := &collector{}
	start := time.Now()
	run(t, newTransport(t, fake), c.sink)
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	if d := time.Since(start); d < time.Second {
		t.Fatalf("event after %v, before retry_after ran out", d)
	}
	if n := fake.Calls("getUpdates"); n < 2 {
		t.Fatalf("getUpdates calls = %d", n)
	}
}

func TestRunRetriesServerErrors(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("getUpdates", 502, 0)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "x"})
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	eventually(t, "an event after a 502", func() bool { return len(c.got()) == 1 })
}

func TestSendSilentToTopic(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.AddMessage(telegramtest.Incoming{ChatID: -100, ThreadID: 45, UserID: 1, Text: "x"})
	tr := newTransport(t, fake)
	if tr.Name() != "telegram" {
		t.Fatalf("Name = %q", tr.Name())
	}
	err := tr.Send(context.Background(), event.Outgoing{IdemKey: "k", Transport: "telegram", Conv: "telegram:-100/45", Text: "hi", Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	want := telegramtest.Outgoing{ChatID: -100, ThreadID: 45, Text: "hi", Silent: true}
	if sent := fake.Sent(); len(sent) != 1 || sent[0] != want {
		t.Fatalf("sent %+v, want %+v", sent, want)
	}
}

func TestSendMapsErrors(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	msg := event.Outgoing{IdemKey: "k", Transport: "telegram", Conv: "telegram:1", Text: "hi"}
	ctx := context.Background()

	fake.FailNext("sendMessage", 429, 7)
	var ra *transport.RetryAfterError
	if err := tr.Send(ctx, msg); !errors.As(err, &ra) || ra.After != 7*time.Second {
		t.Errorf("429: %v, want RetryAfterError 7s", err)
	}
	for _, code := range []int{400, 403} {
		fake.FailNext("sendMessage", code, 0)
		if err := tr.Send(ctx, msg); !errors.Is(err, transport.ErrPermanent) {
			t.Errorf("%d: %v, want ErrPermanent", code, err)
		}
	}
	fake.FailNext("sendMessage", 500, 0)
	err := tr.Send(ctx, msg)
	if err == nil || errors.Is(err, transport.ErrPermanent) || errors.As(err, &ra) {
		t.Errorf("500: %v, want a retryable error", err)
	}
	bad := msg
	bad.Conv = "mqtt:1"
	if err := tr.Send(ctx, bad); !errors.Is(err, transport.ErrPermanent) {
		t.Errorf("bad conv: %v, want ErrPermanent", err)
	}
	if n := len(fake.Sent()); n != 0 {
		t.Fatalf("delivered %d messages through failures", n)
	}
}

func TestNewRejectsMalformedToken(t *testing.T) {
	for _, tok := range []string{"", "secret", ":secret", "abc:secret",
		"7001:abc\n", "7001:abc ", "7001:a b", "7001:abc\r", " 7001:abc", "7001:a/b"} {
		_, err := New(tok)
		if err == nil {
			t.Errorf("New(%q) succeeded", tok)
			continue
		}
		if secret, ok := strings.CutPrefix(tok, "7001:"); ok && strings.Contains(err.Error(), strings.TrimSpace(secret)) {
			t.Errorf("New(%q) error leaks the secret: %v", tok, err)
		}
	}
}

func itoa(v int64) string { return jsonNumber(v) }

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSendUsesEntities(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	if err := tr.Send(context.Background(), event.Outgoing{IdemKey: "k", Transport: "telegram", Conv: "telegram:42", Text: "**Привет**, мир"}); err != nil {
		t.Fatal(err)
	}
	got := fake.Sent()[0]
	if got.Text != "Привет, мир" || got.Entities != `[{"type":"bold","offset":0,"length":6}]` {
		t.Fatalf("sent %+v", got)
	}
}

func TestEventCarriesRef(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	if ref := payload(t, c.got()[0]).Ref; ref != itoa(fake.LastMessageID()) {
		t.Fatalf("ref %q, want the message id %d", ref, fake.LastMessageID())
	}
}

func TestReplyModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   ReplyMode
		chat   int64
		second bool // a newer message arrives before the answer
		want   bool
	}{
		{"auto, private, nothing newer", ReplyAuto, 42, false, false},
		{"auto, private, a newer message", ReplyAuto, 42, true, true},
		{"auto, group", ReplyAuto, -100, false, true},
		{"always", ReplyAlways, 42, false, true},
		{"off", ReplyOff, -100, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := telegramtest.NewServer(t, testToken)
			tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithReplyMode(tc.mode))
			if err != nil {
				t.Fatal(err)
			}
			c := &collector{}
			run(t, tr, c.sink)
			fake.AddMessage(telegramtest.Incoming{ChatID: tc.chat, UserID: 42, Text: "first"})
			n := 1
			if tc.second {
				fake.AddMessage(telegramtest.Incoming{ChatID: tc.chat, UserID: 42, Text: "second"})
				n = 2
			}
			eventually(t, "the messages", func() bool { return len(c.got()) == n })
			ref := payload(t, c.got()[0]).Ref
			if err := tr.Send(context.Background(), event.Outgoing{IdemKey: "r", Transport: "telegram", Conv: c.got()[0].Conv, Text: "answer", ReplyTo: ref}); err != nil {
				t.Fatal(err)
			}
			got := fake.Sent()[0].ReplyTo
			if (got != 0) != tc.want || (tc.want && itoa(got) != ref) {
				t.Fatalf("reply_to %d, want reply=%v to %s", got, tc.want, ref)
			}
		})
	}
}

func TestOnlyTheFirstPartReplies(t *testing.T) {
	tr := &Transport{}
	long := strings.Repeat("слово ", 1000) + "\n\n" + strings.Repeat("ещё ", 1000)
	parts := tr.Parts(event.Outgoing{IdemKey: "k", Text: long, ReplyTo: "5"})
	if len(parts) < 2 || parts[0].ReplyTo != "5" || parts[1].ReplyTo != "" {
		t.Fatalf("parts reply_to: %q, %q", parts[0].ReplyTo, parts[1].ReplyTo)
	}
}

func TestTyping(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	tr.typingEvery = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	tr.Typing(ctx, "telegram:-100/45")
	cancel()
	acts := fake.ChatActions()
	if len(acts) < 3 || acts[0] != (telegramtest.ChatAction{ChatID: -100, ThreadID: 45, Action: "typing"}) {
		t.Fatalf("chat actions %+v", acts)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(fake.ChatActions()); n != len(acts) {
		t.Fatalf("typing went on after ctx ended: %d → %d", len(acts), n)
	}
}

// The turn hook shows "typing…" for a Telegram message until the turn
// ends, and nothing for other events.
func TestTypingHookOnlyForTelegramMessages(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	tr.typingEvery = 20 * time.Millisecond
	ctx := context.Background()
	tr.TypingHook(ctx, event.Event{Source: "mqtt", Kind: event.KindMessage, Conv: "telegram:42"})()
	tr.TypingHook(ctx, event.Event{Source: Name, Kind: "sensor", Conv: "telegram:42"})()
	time.Sleep(60 * time.Millisecond)
	if n := len(fake.ChatActions()); n != 0 {
		t.Fatalf("%d chat actions for events that are not Telegram messages", n)
	}
	stop := tr.TypingHook(ctx, event.Event{Source: Name, Kind: event.KindMessage, Conv: "telegram:42"})
	deadline := time.Now().Add(2 * time.Second)
	for len(fake.ChatActions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	time.Sleep(60 * time.Millisecond)
	n := len(fake.ChatActions())
	time.Sleep(100 * time.Millisecond)
	if n == 0 || len(fake.ChatActions()) != n {
		t.Fatalf("typing: %d actions, then %d after stop", n, len(fake.ChatActions()))
	}
}

var hints = []transport.Command{
	{Name: "new", Description: map[transport.Lang]string{transport.LangDefault: "Start a new conversation", transport.LangRussian: "Новый разговор"}},
	{Name: "sessions", Description: map[transport.Lang]string{transport.LangDefault: "Earlier conversations", transport.LangUkrainian: "Попередні розмови"}},
}

// At start the transport lists the commands for every language they are
// described in: the default without language_code, then each language,
// a command without a description in it falling back to the default
// (rulings R43, R45).
func TestCommandHintsAreRegisteredPerLanguage(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithCommands(hints))
	if err != nil {
		t.Fatal(err)
	}
	run(t, tr, (&collector{}).sink)
	eventually(t, "three command sets", func() bool { return len(fake.CommandSets()) == 3 })
	want := []telegramtest.CommandSet{
		{Language: "", Commands: []telegramtest.BotCommand{{Command: "new", Description: "Start a new conversation"}, {Command: "sessions", Description: "Earlier conversations"}}},
		{Language: "ru", Commands: []telegramtest.BotCommand{{Command: "new", Description: "Новый разговор"}, {Command: "sessions", Description: "Earlier conversations"}}},
		{Language: "uk", Commands: []telegramtest.BotCommand{{Command: "new", Description: "Start a new conversation"}, {Command: "sessions", Description: "Попередні розмови"}}},
	}
	if got := fake.CommandSets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("command sets %+v", got)
	}
}

// A failed setMyCommands is logged and the transport goes on: the other
// languages are still set and messages still arrive.
func TestCommandHintsFailureIsNotFatal(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("setMyCommands", http.StatusInternalServerError, 0)
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithCommands(hints))
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	run(t, tr, c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	eventually(t, "the message", func() bool { return len(c.got()) == 1 })
	if got := fake.CommandSets(); len(got) != 2 || got[0].Language != "ru" || got[1].Language != "uk" {
		t.Fatalf("command sets %+v", got)
	}
}

// Without commands nothing is registered.
func TestNoCommandHintsWithoutCommands(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	eventually(t, "the message", func() bool { return len(c.got()) == 1 })
	if fake.Calls("setMyCommands") != 0 {
		t.Fatal("commands registered without any")
	}
}
