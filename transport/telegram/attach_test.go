package telegram

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\nnot really a picture")

func photo(text string) event.Outgoing {
	return event.Outgoing{IdemKey: "k", Transport: Name, Conv: "telegram:1", Text: text,
		Attach: &event.Attachment{Kind: event.AttachPhoto, Name: "metrics.png", Type: "image/png", Data: pngBytes}}
}

func TestSendPhotoWithCaption(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	if _, err := tr.Send(context.Background(), photo("**Notable:** ok")); err != nil {
		t.Fatal(err)
	}
	ups := fake.Uploads()
	if len(ups) != 1 {
		t.Fatalf("uploads %+v", ups)
	}
	u := ups[0]
	if u.Method != "sendPhoto" || u.ChatID != 1 || u.FileName != "metrics.png" || string(u.Data) != string(pngBytes) {
		t.Fatalf("upload %+v", u)
	}
	if u.Caption != "Notable: ok" || !strings.Contains(u.Entities, `"bold"`) {
		t.Fatalf("caption %q entities %s", u.Caption, u.Entities)
	}
	if n := fake.Calls("sendMessage"); n != 0 {
		t.Fatalf("sendMessage called %d times", n)
	}
}

// A text longer than a caption goes after the picture, as the next parts;
// a crash after the picture resumes at the text and sends the picture once
// (review focus 4).
func TestLongCaptionSplitsIntoParts(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	msg := photo(strings.Repeat("word ", 400)) // 2,000 characters
	parts := tr.Parts(msg)
	if again := tr.Parts(msg); !reflect.DeepEqual(parts, again) {
		t.Fatal("Parts is not deterministic")
	}
	if len(parts) != 2 || parts[0].Attach == nil || parts[0].Text != "" || parts[1].Attach != nil || parts[1].Text == "" {
		t.Fatalf("parts %+v", parts)
	}
	if parts[0].IdemKey != "k:p0" || parts[1].IdemKey != "k:p1" {
		t.Fatalf("keys %q %q", parts[0].IdemKey, parts[1].IdemKey)
	}
	ctx := context.Background()
	if _, err := tr.Send(ctx, parts[0]); err != nil {
		t.Fatal(err)
	}
	fake.FailNext("sendMessage", http.StatusInternalServerError, 0)
	if _, err := tr.Send(ctx, parts[1]); err == nil {
		t.Fatal("the failed text part did not fail")
	}
	if _, err := tr.Send(ctx, parts[1]); err != nil { // the sender resumes at part 1
		t.Fatal(err)
	}
	if n := len(fake.Uploads()); n != 1 {
		t.Fatalf("the picture was sent %d times", n)
	}
	if sent := fake.Sent(); len(sent) != 1 || !strings.HasPrefix(sent[0].Text, "word word") {
		t.Fatalf("text sent %+v", sent)
	}
}

func TestSendDocumentName(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	msg := event.Outgoing{Transport: Name, Conv: "telegram:-100/7",
		Attach: &event.Attachment{Kind: event.AttachDocument, Name: "report.txt", Type: "text/plain", Data: []byte("hello")}}
	if _, err := tr.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	u := fake.Uploads()[0]
	if u.Method != "sendDocument" || u.FileName != "report.txt" || u.ChatID != -100 || u.ThreadID != 7 || u.Caption != "" {
		t.Fatalf("upload %+v", u)
	}
}

func TestAttachmentWithButtonsIsPermanent(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	for name, m := range map[string]event.Outgoing{
		"buttons": func() event.Outgoing { m := photo("x"); m.Buttons = []event.Button{{ID: "b", Label: "B"}}; return m }(),
		"edit":    func() event.Outgoing { m := photo("x"); m.Edit = "5"; return m }(),
		"kind":    func() event.Outgoing { m := photo("x"); m.Attach.Kind = "video"; return m }(),
	} {
		if _, err := tr.Send(context.Background(), m); !errors.Is(err, transport.ErrPermanent) {
			t.Errorf("%s: %v, want ErrPermanent", name, err)
		}
	}
	if n := len(fake.Uploads()); n != 0 {
		t.Fatalf("%d uploads", n)
	}
}

// The token never shows in an upload's error.
func TestUploadErrorRedactsToken(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newTransport(t, fake)
	fake.FailNext("sendPhoto", http.StatusBadRequest, 0)
	_, err := tr.Send(context.Background(), photo("x"))
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error %v", err)
	}
}
