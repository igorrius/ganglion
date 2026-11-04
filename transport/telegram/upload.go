package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"strconv"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
)

// fileParams are the parameters of sendPhoto and sendDocument that we use.
type fileParams struct {
	ChatID   int64
	ThreadID int64
	Caption  string
	Entities []entity
	Silent   bool
	Reply    *replyParameters
	Field    string // "photo" or "document": the form field of the file
	Name     string
	Data     []byte
}

// sendFile uploads a picture or a document (R-DLV-9) as
// multipart/form-data; method is sendPhoto or sendDocument.
func (c *client) sendFile(ctx context.Context, method string, p fileParams) error {
	fields, err := p.fields()
	if err != nil {
		return c.fail(method, err)
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return c.fail(method, err)
		}
	}
	fw, err := w.CreateFormFile(p.Field, p.Name)
	if err != nil {
		return c.fail(method, err)
	}
	if _, err := fw.Write(p.Data); err != nil {
		return c.fail(method, err)
	}
	if err := w.Close(); err != nil {
		return c.fail(method, err)
	}
	return c.do(ctx, method, w.FormDataContentType(), b.Bytes(), nil)
}

// fields are the form fields besides the file, in a fixed order.
func (p fileParams) fields() ([][2]string, error) {
	out := [][2]string{{"chat_id", strconv.FormatInt(p.ChatID, 10)}}
	if p.ThreadID != 0 {
		out = append(out, [2]string{"message_thread_id", strconv.FormatInt(p.ThreadID, 10)})
	}
	if p.Caption != "" {
		out = append(out, [2]string{"caption", p.Caption})
	}
	if len(p.Entities) > 0 {
		raw, err := json.Marshal(p.Entities)
		if err != nil {
			return nil, err
		}
		out = append(out, [2]string{"caption_entities", string(raw)})
	}
	if p.Silent {
		out = append(out, [2]string{"disable_notification", "true"})
	}
	if p.Reply != nil {
		raw, err := json.Marshal(p.Reply)
		if err != nil {
			return nil, err
		}
		out = append(out, [2]string{"reply_parameters", string(raw)})
	}
	return out, nil
}

// fileMethods are the Bot API method and form field of each attachment
// kind.
var fileMethods = map[event.AttachKind][2]string{
	event.AttachPhoto:    {"sendPhoto", "photo"},
	event.AttachDocument: {"sendDocument", "document"},
}

// sendAttachment sends msg's attachment with its text as the caption. An
// attachment with buttons, as an edit or an acknowledgement, of an
// unknown kind, over the size limit or with a caption too long is a
// permanent error: Parts moves a long text out of the caption.
func (t *Transport) sendAttachment(ctx context.Context, chat, thread int64, msg event.Outgoing) error {
	a := msg.Attach
	m, known := fileMethods[a.Kind]
	switch {
	case len(msg.Buttons) > 0 || msg.Edit != "" || msg.Ack != "":
		return fmt.Errorf("telegram: an attachment cannot carry buttons, an edit or a toast: %w", transport.ErrPermanent)
	case !known:
		return fmt.Errorf("telegram: attachment kind %q: %w", a.Kind, transport.ErrPermanent)
	case len(a.Data) > event.MaxAttachmentBytes:
		return fmt.Errorf("telegram: attachment of %d bytes: %w", len(a.Data), transport.ErrPermanent)
	}
	caption, ents := render(msg.Text)
	if textUnits([]rune(caption)) > maxCaptionUnits {
		return fmt.Errorf("telegram: caption too long: %w", transport.ErrPermanent)
	}
	name := a.Name
	if name == "" {
		name = "file"
	}
	return sendError(t.c.sendFile(ctx, m[0], fileParams{
		ChatID: chat, ThreadID: thread, Caption: caption, Entities: ents, Silent: msg.Silent,
		Reply: t.replyTo(chat, msg.ReplyTo), Field: m[1], Name: name, Data: a.Data,
	}))
}
