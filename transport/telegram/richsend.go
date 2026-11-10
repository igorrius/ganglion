package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/igorrius/ganglion/event"
)

// richOutcome is what became of a rich message (R-OBS-8).
type richOutcome string

const (
	richSent       richOutcome = "sent"
	richRefused    richOutcome = "refused"    // Telegram said no; it went the ordinary way
	richRead       richOutcome = "read"       // one that came to the bot was read
	richUnreadable richOutcome = "unreadable" // one that came to the bot held nothing to read
)

// richCounter counts rich messages by outcome.
type richCounter struct{ c metric.Int64Counter }

func newRichCounter() richCounter {
	c, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.telegram.rich",
		metric.WithDescription("Rich messages sent, refused by Telegram and sent the ordinary way, and read from people, by outcome (R-DLV-11, R-DLV-12)"))
	return richCounter{c}
}

func (r richCounter) add(ctx context.Context, o richOutcome) {
	r.c.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}

// richShape reports whether msg may go as a rich message at all: the setting is on,
// nothing later needs it to be ordinary (R-DLV-11), and it is not a module's message,
// which is plain text without previews (R-RUN-16).
func (t *Transport) richShape(msg event.Outgoing) bool {
	return t.rich && msg.Edit == "" && msg.EditOf == "" && msg.Ack == "" && msg.Attach == nil &&
		len(msg.Buttons) == 0 && msg.Folded == "" && !msg.Superseded && !msg.NoPreview
}

// sendsRich reports whether Send sends this part as a rich message: it has the
// structure, or it is too long for an ordinary message, which only the rich
// branch of Parts produces.
func (t *Transport) sendsRich(msg event.Outgoing) bool {
	return t.richShape(msg) && (structured(msg.Text) || renderedUnits(msg.Text) > maxTextUnits)
}

// sendRich sends msg as a rich message, and the ordinary way when Telegram refuses
// it, so that a refusal never loses a message. A 404 is a refusal too: a Bot API
// server older than rich messages does not have the method, and a retry would hold
// the conversation forever. A 429, a 403 and a network error mean the same for
// either way, and are returned as usual.
func (t *Transport) sendRich(ctx context.Context, chat, thread int64, msg event.Outgoing) (string, error) {
	m, err := t.c.sendRichMessage(ctx, richParams{
		ChatID:              chat,
		MessageThreadID:     thread,
		RichMessage:         renderRich(msg.Text),
		DisableNotification: msg.Silent,
		ReplyParameters:     t.replyTo(chat, msg.ReplyTo),
	})
	if err == nil {
		t.richCount.add(ctx, richSent)
		return strconv.FormatInt(m.MessageID, 10), nil
	}
	if ae, ok := errors.AsType[*apiError](err); !ok || (ae.Code != http.StatusBadRequest && ae.Code != http.StatusNotFound) {
		return "", sendError(err)
	}
	t.log.Warn("telegram: rich message refused, sending it the ordinary way", "idem_key", msg.IdemKey, "error", err)
	t.richCount.add(ctx, richRefused)
	return t.sendChunks(ctx, chat, thread, msg)
}

// sendChunks sends msg the ordinary way, in several messages when it does not fit
// one, and returns the last one's reference. Only the first is a reply and only the
// last keeps the buttons. A failure midway is retried from the start, which can
// repeat a chunk but cannot lose one.
func (t *Transport) sendChunks(ctx context.Context, chat, thread int64, msg event.Outgoing) (string, error) {
	pieces := ordinaryPieces(msg.Text)
	ref := ""
	for i, p := range pieces {
		part := msg
		if i > 0 {
			part.ReplyTo = ""
		}
		if i < len(pieces)-1 {
			part.Buttons = nil
		}
		r, err := t.sendText(ctx, chat, thread, part, p.text, p.ents)
		if err != nil {
			return "", err
		}
		ref = r
	}
	return ref, nil
}

// piece is one ordinary message, rendered.
type piece struct {
	text string
	ents []entity
}

// ordinaryPieces renders src into messages that each fit. A chunk that still does
// not fit, such as a table with one row too long for a message, goes out as its
// source, cut: unformatted, but all of it.
func ordinaryPieces(src string) []piece {
	chunks := []string{src}
	if !fitsMessage(src) {
		chunks = splitMarkdown(src)
	}
	var out []piece
	for _, chunk := range chunks {
		text, ents := visible(chunk)
		if textUnits([]rune(text)) <= maxTextUnits {
			out = append(out, piece{text, ents})
			continue
		}
		for _, cut := range cutBlock(chunk) {
			out = append(out, piece{text: cut})
		}
	}
	return out
}
