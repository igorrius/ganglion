package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// testLine is what the page shows for a secret: a well-formed intake line.
const testLine = "gsec1.AAECAwQFBgcICQoLDA0ODw.BDcxMT6NodJ2wPvfbZ2ut62WLNXRz8T7QIVrAFM_-p9Rd5O5zrV-YALy1hCmhOhwBNtXiFokv1uKYG15u9AeBad0GbDb5pjizxJ1Hg3r84UD-ZOt7RNOOkbAbdVV8obQmIiYZmjc-8t5bQ21"

// intakeSpy is an IntakeFunc that records what it was given.
type intakeSpy struct {
	mu      sync.Mutex
	pasted  []intake.Pasted
	outcome intake.TakeOutcome
}

func (s *intakeSpy) take(_ context.Context, p intake.Pasted) (string, intake.TakeOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pasted = append(s.pasted, p)
	return "req-id", s.outcome
}

func (s *intakeSpy) got() []intake.Pasted {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]intake.Pasted(nil), s.pasted...)
}

// safeBuf is a log buffer that the transport's goroutines may write.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func logTo(b *safeBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func intakeTransport(t *testing.T, fake *telegramtest.Server, spy *intakeSpy, logs *safeBuf) *Transport {
	t.Helper()
	opts := []Option{WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithLogger(logTo(logs))}
	if spy != nil {
		opts = append(opts, WithIntake(spy.take))
	}
	tr, err := New(testToken, opts...)
	if err != nil {
		t.Fatal(err)
	}
	tr.retry.Min = 10 * time.Millisecond
	return tr
}

// wantTaken checks what R-INT-5 asks of a taken line: the intake func got
// it, the message is deleted, the sink saw nothing but the message after it,
// and the log never holds the text.
func wantTaken(t *testing.T, fake *telegramtest.Server, c *collector, spy *intakeSpy, logs *safeBuf, chat, msg int64, private bool) {
	t.Helper()
	p := spy.got()
	if len(p) != 1 || p[0].Text != testLine || p[0].Private != private || p[0].From != "telegram:user:42" {
		t.Fatalf("intake got %+v", p)
	}
	d := fake.Deleted()
	if len(d) != 1 || d[0].ChatID != chat || d[0].MessageID != msg {
		t.Fatalf("deleted %+v, want chat %d message %d", d, chat, msg)
	}
	for _, e := range c.got() {
		if strings.Contains(string(e.Payload), "gsec1.") {
			t.Fatalf("the line reached the sink: %s", e.Payload)
		}
	}
	if strings.Contains(logs.String(), testLine[:30]) || strings.Contains(logs.String(), "gsec1.") {
		t.Errorf("the line is in the log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "intake line taken") || !strings.Contains(logs.String(), "request=req-id") {
		t.Errorf("the log lacks the taken line:\n%s", logs.String())
	}
}

func TestPollingTakesAPrivateLine(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{outcome: intake.LineTaken}, &safeBuf{}, &collector{}
	run(t, intakeTransport(t, fake, spy, logs), c.sink)
	upd := fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: testLine})
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "an ordinary message"})
	eventually(t, "the message after the line", func() bool { return len(c.got()) == 1 })
	if payload(t, c.got()[0]).Text != "an ordinary message" {
		t.Errorf("the sink got %+v", c.got())
	}
	eventually(t, "confirmed", func() bool { return fake.Confirmed() > upd })
	wantTaken(t, fake, c, spy, logs, 42, fake.LastMessageID()-1, true)
}

// A line in a group goes to the intake func marked not private (it decides),
// is deleted, and never reaches the sink, addressed to the bot or not.
func TestPollingTakesAGroupLineAndDeletesIt(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{outcome: intake.LineNotPrivate}, &safeBuf{}, &collector{}
	run(t, intakeTransport(t, fake, spy, logs), c.sink)
	upd := fake.AddMessage(telegramtest.Incoming{ChatID: -100500, UserID: 42, Title: "Team", Text: testLine})
	eventually(t, "confirmed", func() bool { return fake.Confirmed() >= upd })
	wantTaken(t, fake, c, spy, logs, -100500, fake.LastMessageID(), false)
	if len(c.got()) != 0 {
		t.Errorf("the sink got %+v", c.got())
	}
}

// With no intake configured the line is still taken, deleted and kept from the sink.
func TestALineIsTakenWithNoIntakeConfigured(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	logs, c := &safeBuf{}, &collector{}
	run(t, intakeTransport(t, fake, nil, logs), c.sink)
	upd := fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: testLine})
	eventually(t, "confirmed", func() bool { return fake.Confirmed() >= upd })
	if len(fake.Deleted()) != 1 || len(c.got()) != 0 {
		t.Fatalf("deleted %v, sink %v", fake.Deleted(), c.got())
	}
	if !strings.Contains(logs.String(), "outcome=dropped") || strings.Contains(logs.String(), "gsec1.") {
		t.Errorf("log:\n%s", logs.String())
	}
}

// A line in a caption, after other words, in another case, behind a
// zero-width character or forwarded is taken all the same: deleted, kept
// from the sink, and handed to the intake func marked, which refuses it
// with a fixed text (R-INT-5).
func TestALineAnywhereIsTaken(t *testing.T) {
	doc := map[string]any{"document": map[string]any{"file_name": "a.txt", "file_size": 3}}
	fwd := map[string]any{"forward_origin": map[string]any{"type": "hidden_user", "date": 1, "sender_user_name": "x"}}
	for name, tc := range map[string]struct {
		in   telegramtest.Incoming
		want func(intake.Pasted) bool
	}{
		"caption":     {telegramtest.Incoming{Caption: testLine, Extra: doc}, func(p intake.Pasted) bool { return p.Caption && p.Text == testLine }},
		"words first": {telegramtest.Incoming{Text: "my token is " + testLine}, func(p intake.Pasted) bool { return !p.Caption && !p.Forwarded }},
		"capitalised": {telegramtest.Incoming{Text: "G" + testLine[1:]}, func(p intake.Pasted) bool { return !p.Caption }},
		"zero-width":  {telegramtest.Incoming{Text: "\u200b" + testLine}, func(p intake.Pasted) bool { return !p.Caption }},
		"forwarded":   {telegramtest.Incoming{Text: testLine, Extra: fwd}, func(p intake.Pasted) bool { return p.Forwarded }},
	} {
		t.Run(name, func(t *testing.T) {
			fake := telegramtest.NewServer(t, testToken)
			spy, logs, c := &intakeSpy{outcome: intake.LineNotAlone}, &safeBuf{}, &collector{}
			run(t, intakeTransport(t, fake, spy, logs), c.sink)
			tc.in.ChatID, tc.in.UserID = 42, 42
			upd := fake.AddMessage(tc.in)
			eventually(t, "confirmed", func() bool { return fake.Confirmed() >= upd })
			if p := spy.got(); len(p) != 1 || !p[0].Private || !tc.want(p[0]) {
				t.Fatalf("intake got %+v", p)
			}
			if len(fake.Deleted()) != 1 || len(c.got()) != 0 {
				t.Fatalf("deleted %v, sink %v", fake.Deleted(), c.got())
			}
			if strings.Contains(logs.String(), testLine[10:40]) {
				t.Errorf("the line is in the log:\n%s", logs.String())
			}
		})
	}
}

// The message is deleted before the intake func runs, so a slow one never
// leaves the line in the chat.
func TestTheLineIsDeletedBeforeTheIntakeRuns(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	var deletedFirst, ran atomic.Bool
	take := func(context.Context, intake.Pasted) (string, intake.TakeOutcome) {
		deletedFirst.Store(len(fake.Deleted()) == 1)
		ran.Store(true)
		return "", intake.LineTaken
	}
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithIntake(take))
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	run(t, tr, c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: testLine})
	eventually(t, "the intake ran", ran.Load)
	if !deletedFirst.Load() {
		t.Fatal("the intake ran before the message was deleted")
	}
}

// Abuse: a line the bot could not delete never reaches the inbox through a
// later reply to it or a quote of it.
func TestAReplyOrQuoteOfALineHidesIt(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, intakeTransport(t, fake, &intakeSpy{}, &safeBuf{}), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "about that", Extra: map[string]any{
		"reply_to_message": map[string]any{"message_id": 7, "date": 1, "chat": map[string]any{"id": 42, "type": "private"},
			"from": map[string]any{"id": 42, "is_bot": false, "first_name": "A"}, "text": "see " + testLine},
		"quote": map[string]any{"text": testLine, "position": 0},
	}})
	eventually(t, "the event", func() bool { return len(c.got()) == 1 })
	m := payload(t, c.got()[0])
	if strings.Contains(string(c.got()[0].Payload), "gsec1.") || m.Quote != removedLine || m.Reply == nil || m.Reply.Text != removedLine {
		t.Fatalf("payload %s", c.got()[0].Payload)
	}
}

// A message that only names the format stays an ordinary message.
func TestOrdinaryTextIsNotTaken(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{}, &safeBuf{}, &collector{}
	run(t, intakeTransport(t, fake, spy, logs), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "gsec1 is a format, gsec1.<id> its shape"})
	eventually(t, "one event", func() bool { return len(c.got()) == 1 })
	if len(spy.got()) != 0 || len(fake.Deleted()) != 0 {
		t.Errorf("taken: %v %v", spy.got(), fake.Deleted())
	}
}

// Abuse: a failed deletion (the bot may not delete in this chat) still
// keeps the line from the sink and out of the log, and logs the error code.
func TestAFailedDeletionStillKeepsTheLineFromTheSink(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{outcome: intake.LineNotPrivate}, &safeBuf{}, &collector{}
	fake.FailNext("deleteMessage", http.StatusForbidden, 0)
	run(t, intakeTransport(t, fake, spy, logs), c.sink)
	upd := fake.AddMessage(telegramtest.Incoming{ChatID: -100500, UserID: 42, Title: "Team", Text: testLine})
	eventually(t, "confirmed", func() bool { return fake.Confirmed() >= upd })
	if len(c.got()) != 0 || len(fake.Deleted()) != 0 {
		t.Fatalf("sink %v, deleted %v", c.got(), fake.Deleted())
	}
	if !strings.Contains(logs.String(), "not deleted") || !strings.Contains(logs.String(), "code=403") || strings.Contains(logs.String(), "gsec1.") {
		t.Errorf("log:\n%s", logs.String())
	}
}

// A bot's message is no person's: it is not taken.
func TestABotsLineIsNotTaken(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{}, &safeBuf{}, &collector{}
	run(t, intakeTransport(t, fake, spy, logs), c.sink)
	upd := fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 43, IsBot: true, Text: testLine})
	eventually(t, "confirmed", func() bool { return fake.Confirmed() >= upd })
	if len(spy.got()) != 0 || len(fake.Deleted()) != 0 {
		t.Errorf("taken: %v %v", spy.got(), fake.Deleted())
	}
}

func TestWebhookTakesALine(t *testing.T) {
	h := newHooked(t, reachable)
	spy, logs, c := &intakeSpy{outcome: intake.LineTaken}, &safeBuf{}, &collector{}
	tr, err := New(testToken, WithBaseURL(h.fake.URL()), WithPollTimeout(time.Second), WithHealth(h.health), WithLogger(logTo(logs)),
		WithWebhook(h.front.URL+"/", reachable), WithWebhookRecord(h.record), WithIntake(spy.take))
	if err != nil {
		t.Fatal(err)
	}
	tr.retry.Min, tr.hook.every = 10*time.Millisecond, time.Hour
	h.tr = tr
	serve(t, tr, c.sink)
	h.registered(t)
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: testLine})
	h.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "after"})
	eventually(t, "the message after", func() bool { return len(c.got()) == 1 })
	eventually(t, "all confirmed", func() bool { return h.fake.WebhookInfo().PendingUpdateCount == 0 })
	wantTaken(t, h.fake, c, spy, logs, 42, h.fake.LastMessageID()-1, true)
}

// A group's line through the webhook is taken, deleted and answered 200, so
// Telegram does not send it again.
func TestWebhookTakesAGroupLine(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	spy, logs, c := &intakeSpy{outcome: intake.LineNotPrivate}, &safeBuf{}, &collector{}
	tr := intakeTransport(t, fake, spy, logs)
	tr.hook.open(user{ID: 7001, IsBot: true, Username: telegramtest.BotUsername}, c.sink)
	srv := httptest.NewServer(tr.WebhookHandler())
	t.Cleanup(srv.Close)
	body := `{"update_id":9,"message":{"message_id":5,"date":1,"chat":{"id":-100500,"type":"supergroup"},"from":{"id":42,"is_bot":false,"first_name":"A"},"text":"` + testLine + `"}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set(secretHeader, string(tr.hook.secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if d := fake.Deleted(); len(d) != 1 || d[0].ChatID != -100500 || d[0].MessageID != 5 {
		t.Fatalf("deleted %+v", d)
	}
	if p := spy.got(); len(p) != 1 || p[0].Private || p[0].Text != testLine {
		t.Fatalf("intake got %+v", p)
	}
	if len(c.got()) != 0 || strings.Contains(logs.String(), "gsec1.") {
		t.Errorf("sink %v, log:\n%s", c.got(), logs.String())
	}
}
