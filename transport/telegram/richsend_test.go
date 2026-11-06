package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const tableMD = "| a | b |\n|---|---|\n| 1 | 2 |"

func newRichTransport(t *testing.T, fake *telegramtest.Server) *Transport {
	t.Helper()
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithRichMessages(true))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func richOut(text string) event.Outgoing {
	return event.Outgoing{IdemKey: "k", Transport: Name, Conv: "telegram:42", Text: text}
}

func TestRichSentIsCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	fake := telegramtest.NewServer(t, testToken)
	if _, err := newRichTransport(t, fake).Send(context.Background(), richOut(tableMD)); err != nil {
		t.Fatal(err)
	}
	if got := rec.Value(t, "ganglion.telegram.rich", map[string]string{"outcome": "sent"}); got != 1 {
		t.Fatalf("sent metric = %v", got)
	}
}

func TestSendRichWhenStructured(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		rich       bool
	}{
		{"a table", tableMD, true},
		{"two headings", "# a\n\ntext\n\n# b", true},
		{"one heading", "# a\n\ntext", false},
		{"plain", "text", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := telegramtest.NewServer(t, testToken)
			ref, err := newRichTransport(t, fake).Send(context.Background(), richOut(tc.text))
			if err != nil || ref == "" {
				t.Fatalf("ref %q, err %v", ref, err)
			}
			sent := fake.Sent()
			if len(sent) != 1 || sent[0].Rich != tc.rich {
				t.Fatalf("sent %+v, want rich %v", sent, tc.rich)
			}
		})
	}
}

func TestSendRichCarriesThreadAndSilence(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.AddMessage(telegramtest.Incoming{ChatID: -100, ThreadID: 7, UserID: 1, Text: "x"})
	msg := richOut(tableMD)
	msg.Conv, msg.Silent = "telegram:-100/7", true
	if _, err := newRichTransport(t, fake).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := fake.Sent()[0]
	if !got.Rich || got.ThreadID != 7 || !got.Silent || !strings.Contains(got.Text, "1 | 2") {
		t.Fatalf("sent %+v", got)
	}
}

func TestSendRichCarriesReply(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithRichMessages(true), WithReplyMode(ReplyAlways))
	if err != nil {
		t.Fatal(err)
	}
	msg := richOut(tableMD)
	msg.ReplyTo = "9"
	if _, err := tr.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got := fake.Sent()[0]; !got.Rich || got.ReplyTo != 9 {
		t.Fatalf("sent %+v", got)
	}
}

func TestRichExclusions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(m *event.Outgoing)
	}{
		{"buttons", func(m *event.Outgoing) { m.Buttons = []event.Button{{ID: "b", Label: "OK"}} }},
		{"superseded", func(m *event.Outgoing) { m.Superseded = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := telegramtest.NewServer(t, testToken)
			msg := richOut(tableMD)
			tc.edit(&msg)
			if _, err := newRichTransport(t, fake).Send(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if fake.Calls("sendRichMessage") != 0 || fake.Calls("sendMessage") != 1 {
				t.Fatalf("methods %v", fake.Methods())
			}
		})
	}
}

func TestRichShapeExcludes(t *testing.T) {
	tr := &Transport{rich: true}
	if !tr.richShape(richOut(tableMD)) {
		t.Fatal("a plain message is refused")
	}
	for name, edit := range map[string]func(m *event.Outgoing){
		"Edit":       func(m *event.Outgoing) { m.Edit = "1" },
		"EditOf":     func(m *event.Outgoing) { m.EditOf = "k" },
		"Ack":        func(m *event.Outgoing) { m.Ack = "q" },
		"Attach":     func(m *event.Outgoing) { m.Attach = &event.Attachment{} },
		"Buttons":    func(m *event.Outgoing) { m.Buttons = []event.Button{{ID: "b"}} },
		"Folded":     func(m *event.Outgoing) { m.Folded = "q" },
		"Superseded": func(m *event.Outgoing) { m.Superseded = true },
	} {
		msg := richOut(tableMD)
		edit(&msg)
		if tr.richShape(msg) {
			t.Errorf("%s is not excluded", name)
		}
	}
	if (&Transport{}).richShape(richOut(tableMD)) {
		t.Error("the setting off is not respected")
	}
}

func TestRichOff(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	if _, err := newTransport(t, fake).Send(context.Background(), richOut(tableMD)); err != nil {
		t.Fatal(err)
	}
	if fake.Calls("sendRichMessage") != 0 || fake.Sent()[0].Rich {
		t.Fatalf("methods %v", fake.Methods())
	}
}

// A plan's first text is the shape of plan.Render: its edits use plain text, so
// it must not start as a rich message.
func TestPlanStaysOrdinary(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	plan := "✅ 1. # first | step\n⏳ 2. ## second | step\n· 3. |---|\n· 4. | a |"
	if _, err := newRichTransport(t, fake).Send(context.Background(), richOut(plan)); err != nil {
		t.Fatal(err)
	}
	if fake.Calls("sendRichMessage") != 0 {
		t.Fatalf("methods %v", fake.Methods())
	}
}

func TestRichRefusedFallsBack(t *testing.T) {
	rec := telemetrytest.Install(t)
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("sendRichMessage", 400, 0)
	ref, err := newRichTransport(t, fake).Send(context.Background(), richOut(tableMD))
	if err != nil || ref == "" {
		t.Fatalf("ref %q, err %v", ref, err)
	}
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].Rich || !strings.Contains(sent[0].Text, "1 | 2") {
		t.Fatalf("sent %+v", sent)
	}
	if got := rec.Value(t, "ganglion.telegram.rich", map[string]string{"outcome": "refused"}); got != 1 {
		t.Fatalf("refused metric = %v", got)
	}
}

func TestRichRefusedLongSplits(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("sendRichMessage", 400, 0)
	var src strings.Builder
	src.WriteString(tableMD)
	want := 0
	for src.Len() < 10000 {
		want++
		src.WriteString("\n\npara" + strings.Repeat("x", 500))
	}
	msg := richOut(src.String())
	msg.ReplyTo = "9"
	ref, err := newRichTransport(t, fake).Send(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	if len(sent) < 2 {
		t.Fatalf("%d messages", len(sent))
	}
	all := ""
	for _, s := range sent {
		if s.Rich {
			t.Fatal("a chunk went rich")
		}
		all += s.Text
	}
	if got := strings.Count(all, "para"); got != want {
		t.Fatalf("%d paragraphs of %d", got, want)
	}
	if last := fake.LastMessageID(); ref != itoa(last) {
		t.Fatalf("ref %q, last message %d", ref, last)
	}
}

func TestRichRetryableNoFallback(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newRichTransport(t, fake)
	fake.FailNext("sendRichMessage", 429, 7)
	_, err := tr.Send(context.Background(), richOut(tableMD))
	if _, ok := errors.AsType[*transport.RetryAfterError](err); !ok {
		t.Fatalf("err %v", err)
	}
	fake.FailNext("sendRichMessage", 500, 0)
	if _, err = tr.Send(context.Background(), richOut(tableMD)); err == nil || errors.Is(err, transport.ErrPermanent) {
		t.Fatalf("err %v", err)
	}
	if fake.Calls("sendMessage") != 0 {
		t.Fatalf("methods %v", fake.Methods())
	}
}

func TestRichThreadGoneFailsOnce(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	msg := richOut(tableMD)
	msg.Conv = "telegram:-100/7" // a topic the fake does not have
	_, err := newRichTransport(t, fake).Send(context.Background(), msg)
	if !errors.Is(err, transport.ErrPermanent) {
		t.Fatalf("err %v", err)
	}
	if len(fake.Sent()) != 0 || fake.Calls("sendRichMessage") != 1 || fake.Calls("sendMessage") != 1 {
		t.Fatalf("sent %+v, methods %v", fake.Sent(), fake.Methods())
	}
}

// A server older than rich messages answers 404: retrying would hold the
// conversation forever.
func TestRichMethodMissingFallsBack(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("sendRichMessage", 404, 0)
	if _, err := newRichTransport(t, fake).Send(context.Background(), richOut(tableMD)); err != nil {
		t.Fatal(err)
	}
	if sent := fake.Sent(); len(sent) != 1 || sent[0].Rich {
		t.Fatalf("sent %+v", sent)
	}
}

func wideTable(rows int) string {
	var b strings.Builder
	b.WriteString("| a | b |\n|---|---|\n| " + strings.Repeat("w", 1500) + " | 1 |")
	for i := range rows {
		fmt.Fprintf(&b, "\n| row%d | %d |", i, i)
	}
	return b.String()
}

// The source is short, but an ordinary message pads every row to the widest cell.
func TestRichRefusedWideTableIsDelivered(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("sendRichMessage", 400, 0)
	if _, err := newRichTransport(t, fake).Send(context.Background(), richOut(wideTable(20))); err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, s := range fake.Sent() {
		all += s.Text + "\n"
	}
	for i := range 20 {
		if !strings.Contains(all, fmt.Sprintf("row%d ", i)) {
			t.Fatalf("row %d is lost", i)
		}
	}
}

// One row that alone cannot fit an ordinary message goes out as its source.
func TestRichRefusedHugeRowIsDelivered(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.FailNext("sendRichMessage", 400, 0)
	huge := strings.Repeat("h", 5000)
	if _, err := newRichTransport(t, fake).Send(context.Background(), richOut("| a |\n|---|\n| "+huge+" |")); err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, s := range fake.Sent() {
		all += s.Text
	}
	if strings.Count(all, "h") < 5000 {
		t.Fatalf("%d of 5000 characters delivered", strings.Count(all, "h"))
	}
}

// The way out, the setting off, must deliver the same table.
func TestRichOffWideTableIsDelivered(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	for _, p := range tr.Parts(richOut(wideTable(20))) {
		if _, err := tr.Send(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.Sent()) == 0 {
		t.Fatal("nothing sent")
	}
}
