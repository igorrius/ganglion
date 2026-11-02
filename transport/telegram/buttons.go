package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
)

// callbackPrefix starts the callback data of our buttons: "c:" + the
// button id. The data never names a tool or carries arguments; what a
// button does is stored in the core (ADR-0032).
const callbackPrefix = "c:"

// buttonIDPattern is a button id: 26 characters of Crockford base32.
var buttonIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// answerTimeout bounds answerCallbackQuery, which runs inside the poll
// loop.
const answerTimeout = 5 * time.Second

// notModified is how the Bot API says an edit changes nothing: the
// message already shows it, so the edit is done.
const notModified = "message is not modified"

// answerCallback answers a button press at once and without text, so the
// person's client stops its spinner. Every press is answered, also one
// that becomes no event or that the sink drops; a failure is only logged.
func (t *Transport) answerCallback(ctx context.Context, u update) {
	cq := u.CallbackQuery
	if cq == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, answerTimeout)
	defer cancel()
	if err := t.c.answerCallbackQuery(actx, cq.ID); err != nil && ctx.Err() == nil {
		t.log.Debug("telegram: callback not answered", "update_id", u.UpdateID, "err", err)
	}
}

// decisionEvent turns a press of one of our buttons into a KindDecision
// event in the conversation of the message holding the button. Its
// IdemKey is the update's, like a message's, so a redelivered update is
// stored once.
func (t *Transport) decisionEvent(botID int64, u update) (event.Event, bool) {
	cq := u.CallbackQuery
	id, ok := strings.CutPrefix(cq.Data, callbackPrefix)
	if !ok || !buttonIDPattern.MatchString(id) || cq.From.IsBot {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "not one of our buttons")
		return event.Event{}, false
	}
	m := cq.Message
	if m == nil || m.Date == 0 {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "button on a message no longer accessible")
		return event.Event{}, false
	}
	var thread int64
	if m.IsTopicMessage {
		thread = m.MessageThreadID
	}
	payload, err := json.Marshal(event.Decision{
		Button: id,
		From:   Principal(cq.From.ID),
		Name:   strings.TrimSpace(cq.From.FirstName + " " + cq.From.LastName),
		Ref:    strconv.FormatInt(m.MessageID, 10),
	})
	if err != nil {
		return event.Event{}, false // a struct of strings always marshals
	}
	now := t.now()
	return event.Event{
		ID: event.NewID(now), IdemKey: idemKey(botID, u.UpdateID), Source: Name, Conv: ConvKey(m.Chat.ID, thread),
		Kind: event.KindDecision, Payload: payload, At: now,
	}, true
}

// idemKey is the IdemKey of an update's event: "telegram:<bot>:<update_id>".
func idemKey(botID, updateID int64) string {
	return "telegram:" + strconv.FormatInt(botID, 10) + ":" + strconv.FormatInt(updateID, 10)
}

// keyboard is the inline keyboard of buttons, one per row; nil for none.
func keyboard(buttons []event.Button) *inlineMarkup {
	if len(buttons) == 0 {
		return nil
	}
	return &inlineMarkup{InlineKeyboard: rows(buttons)}
}

// rows are the keyboard rows of buttons, one button each; never nil, so
// an edit with no buttons sends an empty keyboard and removes them.
func rows(buttons []event.Button) [][]inlineButton {
	out := make([][]inlineButton, 0, len(buttons))
	for _, b := range buttons {
		out = append(out, []inlineButton{{Text: b.Label, CallbackData: callbackPrefix + b.ID}})
	}
	return out
}

// edit changes message ref in chat to text with buttons (none removes
// them). "Message is not modified" is success; a ref that is not a
// message id is a permanent failure.
func (t *Transport) edit(ctx context.Context, chat int64, ref, text string, ents []entity, buttons []event.Button) error {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("telegram: edit: message ref %q is not a message id: %w", ref, transport.ErrPermanent)
	}
	err = t.c.editMessageText(ctx, editParams{ChatID: chat, MessageID: id, Text: text, Entities: ents,
		ReplyMarkup: inlineMarkup{InlineKeyboard: rows(buttons)}})
	if ae, ok := errors.AsType[*apiError](err); ok && ae.Code == http.StatusBadRequest && strings.Contains(ae.Description, notModified) {
		return nil
	}
	return err
}
