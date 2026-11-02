package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const buttonID = "01JCQ4ZK7Y3V8M2N6P9R5T1W0X"

func TestCallbackBecomesDecisionEvent(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	up := fake.AddCallback(telegramtest.Callback{ChatID: -100, ThreadID: 45, UserID: 42, Name: "Igor", MessageID: 17, Data: "c:" + buttonID})
	eventually(t, "a decision event", func() bool { return len(c.got()) == 1 })
	e := c.got()[0]
	if e.Kind != event.KindDecision || e.Source != "telegram" || e.Conv != "telegram:-100/45" || e.ID == "" || e.At.IsZero() ||
		e.IdemKey != "telegram:7001:"+strconv.FormatInt(up, 10) {
		t.Fatalf("event %+v", e)
	}
	var d event.Decision
	if err := json.Unmarshal(e.Payload, &d); err != nil {
		t.Fatal(err)
	}
	if d != (event.Decision{Button: buttonID, From: "telegram:user:42", Name: "Igor", Ref: "17"}) {
		t.Fatalf("decision %+v", d)
	}
	if got := fake.AllowedUpdates(); !reflect.DeepEqual(got, []string{"message", "callback_query"}) {
		t.Fatalf("allowed_updates %v", got)
	}
}

// Callback data that is not one of our buttons, and a press on a message
// Telegram no longer shows, become no event; the offset still moves on.
func TestForeignCallbackDataIsSkipped(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	for _, cb := range []telegramtest.Callback{
		{ChatID: 42, UserID: 42, MessageID: 5, Data: "schedule.delete:j1"},
		{ChatID: 42, UserID: 42, MessageID: 5, Data: "c:short"},
		{ChatID: 42, UserID: 42, MessageID: 5, Data: "c:" + strings.ToLower(buttonID)},
		{ChatID: 42, UserID: 42, MessageID: 0, Data: "c:" + buttonID},
	} {
		fake.AddCallback(cb)
	}
	last := fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "after"})
	eventually(t, "the message after the callbacks", func() bool { return len(c.got()) == 1 })
	if c.got()[0].Kind != event.KindMessage {
		t.Fatalf("events %+v", c.got())
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "next"})
	eventually(t, "offset past the callbacks", func() bool { return fake.Confirmed() >= last })
}

// Every callback is answered at once, so the client stops its spinner,
// also when the sink drops it (a stranger's press) or it is not ours.
func TestAnswerCallbackQueryCalled(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	drop := func(context.Context, event.Event) error { return nil }
	run(t, newTransport(t, fake), drop)
	fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 7, MessageID: 5, Data: "c:" + buttonID})
	fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 7, MessageID: 5, Data: "nonsense"})
	eventually(t, "both answered", func() bool { return len(fake.Answered()) == 2 })
	if got := fake.Answered(); got[0] != "cq1" || got[1] != "cq2" {
		t.Fatalf("answered %v", got)
	}
}

func TestSendWithButtons(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	err := tr.Send(context.Background(), event.Outgoing{IdemKey: "k", Transport: "telegram", Conv: "telegram:42", Text: "Run web.fetch?",
		Buttons: []event.Button{{ID: "B1", Label: "Approve"}, {ID: "B2", Label: "Deny"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[[{"text":"Approve","callback_data":"c:B1"}],[{"text":"Deny","callback_data":"c:B2"}]]`
	if got := fake.Sent()[0]; got.Text != "Run web.fetch?" || got.Keyboard != want {
		t.Fatalf("sent %+v\nwant keyboard %s", got, want)
	}
}

// sendOne sends a message with a button and returns its ref.
func sendOne(t *testing.T, tr *Transport, fake *telegramtest.Server) string {
	t.Helper()
	if err := tr.Send(context.Background(), event.Outgoing{IdemKey: "q", Transport: "telegram", Conv: "telegram:42", Text: "Run?",
		Buttons: []event.Button{{ID: "B1", Label: "Approve"}}}); err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(fake.LastMessageID(), 10)
}

func TestEditMessage(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	ref := sendOne(t, tr, fake)
	if err := tr.Send(context.Background(), event.Outgoing{IdemKey: "e", Transport: "telegram", Conv: "telegram:42", Text: "Run?\n\n**Approved.**", Edit: ref}); err != nil {
		t.Fatal(err)
	}
	edits := fake.Edits()
	if len(edits) != 1 || edits[0].ChatID != 42 || strconv.FormatInt(edits[0].MessageID, 10) != ref || edits[0].Text != "Run?\n\nApproved." ||
		edits[0].Entities == "" || edits[0].Keyboard != "" {
		t.Fatalf("edits %+v", edits)
	}
	if len(fake.Sent()) != 1 {
		t.Fatalf("an edit sent a new message: %+v", fake.Sent())
	}
}

func TestEditNotModifiedIsSuccess(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	ref := sendOne(t, tr, fake)
	edit := event.Outgoing{IdemKey: "e", Transport: "telegram", Conv: "telegram:42", Text: "Approved.", Edit: ref}
	for i := range 2 {
		if err := tr.Send(context.Background(), edit); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	if n := len(fake.Edits()); n != 1 {
		t.Fatalf("%d edits took, want 1", n)
	}
}

// A message that is gone cannot be edited, however often it is retried.
func TestEditOfMissingMessageIsPermanent(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	for _, ref := range []string{"999", "not-a-number"} {
		err := tr.Send(context.Background(), event.Outgoing{IdemKey: "e", Transport: "telegram", Conv: "telegram:42", Text: "x", Edit: ref})
		if !errors.Is(err, transport.ErrPermanent) {
			t.Errorf("edit of %q: %v, want ErrPermanent", ref, err)
		}
	}
}

// Buttons stay with the last part of a long message; an edit is never
// split.
func TestPartsKeepButtonsOnTheLastPart(t *testing.T) {
	tr := &Transport{}
	long := strings.Repeat("word ", 2000)
	parts := tr.Parts(event.Outgoing{IdemKey: "k", Text: long, Buttons: []event.Button{{ID: "B", Label: "Approve"}}})
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		if want := i == len(parts)-1; (len(p.Buttons) == 1) != want {
			t.Errorf("part %d buttons %v", i, p.Buttons)
		}
	}
	if edit := tr.Parts(event.Outgoing{IdemKey: "k", Text: long, Edit: "5"}); len(edit) != 1 {
		t.Fatalf("an edit was split into %d parts", len(edit))
	}
}
