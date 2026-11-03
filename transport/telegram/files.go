package telegram

import (
	"context"

	"github.com/igorrius/ganglion/event"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// photoType is the type Telegram stores every photo in.
const photoType = "image/jpeg"

// files describes the files a message carries. Telegram sends an
// animation as a document too, for old clients; it is one file.
func files(m *message) []event.File {
	var out []event.File
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1] // sizes come smallest first
		out = append(out, event.File{Kind: event.FilePhoto, Type: photoType, Size: p.FileSize})
	}
	for _, f := range []struct {
		kind event.FileKind
		info *fileInfo
	}{
		{event.FileDocument, m.Document}, {event.FileAudio, m.Audio}, {event.FileVoice, m.Voice},
		{event.FileVideo, m.Video}, {event.FileVideoNote, m.VideoNote}, {event.FileAnimation, m.Animation},
		{event.FileSticker, m.Sticker},
	} {
		if f.info == nil || (f.kind == event.FileDocument && m.Animation != nil) {
			continue
		}
		out = append(out, event.File{Kind: f.kind, Name: f.info.FileName, Type: f.info.MimeType, Size: f.info.FileSize})
	}
	return out
}

// fileCounter counts the files people send, by kind (R-OBS-8).
type fileCounter struct{ c metric.Int64Counter }

func newFileCounter() fileCounter {
	c, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.files.received",
		metric.WithDescription("Files people sent to the bot, by kind; they are noticed, not opened (R-DLV-10)"))
	return fileCounter{c}
}

func (f fileCounter) add(ctx context.Context, fs []event.File) {
	for _, file := range fs {
		f.c.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(file.Kind))))
	}
}
