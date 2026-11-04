package telegram

import (
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// R-DLV-10: a photo without a caption used to be skipped as "no text".
// It is an event now, with the file noticed and no text.
func TestPhotoWithoutCaptionIsAnEvent(t *testing.T) {
	rec := telemetrytest.Install(t)
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Extra: map[string]any{
		"photo": []map[string]any{
			{"file_id": "small", "file_unique_id": "s", "width": 90, "height": 90, "file_size": 1200},
			{"file_id": "big", "file_unique_id": "b", "width": 1280, "height": 1280, "file_size": 98765},
		},
	}})
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	m := payload(t, c.got()[0])
	if m.Text != "" {
		t.Fatalf("text %q, want none", m.Text)
	}
	want := []event.File{{Kind: event.FilePhoto, Type: "image/jpeg", Size: 98765}}
	if len(m.Files) != 1 || m.Files[0] != want[0] {
		t.Fatalf("files %+v, want %+v", m.Files, want)
	}
	if got := rec.Value(t, "ganglion.files.received", map[string]string{"kind": "photo"}); got != 1 {
		t.Fatalf("files received metric = %v", got)
	}
}

func TestDocumentCarriesNameTypeSize(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Caption: "the invoice", Extra: map[string]any{
		"document": map[string]any{"file_id": "d", "file_unique_id": "d", "file_name": "invoice.pdf",
			"mime_type": "application/pdf", "file_size": 1234567},
	}})
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Extra: map[string]any{
		"voice": map[string]any{"file_id": "v", "file_unique_id": "v", "duration": 3, "mime_type": "audio/ogg", "file_size": 4000},
	}})
	eventually(t, "two events", func() bool { return len(c.got()) == 2 })
	doc := payload(t, c.got()[0])
	if doc.Text != "the invoice" {
		t.Fatalf("caption %q", doc.Text)
	}
	if want := (event.File{Kind: event.FileDocument, Name: "invoice.pdf", Type: "application/pdf", Size: 1234567}); len(doc.Files) != 1 || doc.Files[0] != want {
		t.Fatalf("document %+v, want %+v", doc.Files, want)
	}
	if v := payload(t, c.got()[1]).Files; len(v) != 1 || v[0].Kind != event.FileVoice || v[0].Type != "audio/ogg" {
		t.Fatalf("voice %+v", v)
	}
}
