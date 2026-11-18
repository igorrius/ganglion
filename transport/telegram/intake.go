package telegram

import (
	"cmp"
	"context"
	"errors"

	"github.com/igorrius/ganglion/intake"
)

// IntakeFunc takes one pasted intake line and returns the id of the request
// it named ("" when none could be read) and what became of it
// (intake.Registry.Take). It must never return or log the line.
type IntakeFunc func(ctx context.Context, p intake.Pasted) (id string, outcome intake.TakeOutcome)

// dropIntake is the default when no intake is configured: the line is
// still taken out of the inbox path and deleted, and goes nowhere.
func dropIntake(context.Context, intake.Pasted) (string, intake.TakeOutcome) {
	return "", intake.LineDropped
}

// WithIntake sets who takes pasted gauth1 and gsec1 lines (R-INT-5). The
// default drops them: a line never reaches the inbox either way.
func WithIntake(f IntakeFunc) Option { return func(c *config) { c.intake = f } }

// removedLine stands in a reply or a quote for text that holds an intake
// line, so a line the bot could not delete never reaches the inbox through
// a later message (R-INT-5, R-INT-7).
const removedLine = "[an intake line, removed]"

// hideLine returns s, or removedLine when s holds an intake line.
func hideLine(s string) string {
	if intake.Holds(s) {
		return removedLine
	}
	return s
}

// intercept takes an update whose text or caption holds an intake line
// anywhere out of the inbox path and reports whether it did. The message
// is deleted first, in a group too when the bot can (R-INT-5), so the line
// leaves the chat at once; then it goes to the intake func with the sender,
// whether the chat is private, and whether it is a caption or forwarded
// (the intake decides who may, and refuses anything but the admin's own
// text message with the line alone). The update counts as handled and the
// sink never sees it. The text is never logged: the Info line holds the
// update id, the request id and the outcome.
func (t *Transport) intercept(ctx context.Context, u update) bool {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || (!intake.Holds(m.Text) && !intake.Holds(m.Caption)) {
		return false
	}
	t.deleteLine(ctx, u.UpdateID, m)
	id, outcome := t.intake(ctx, intake.Pasted{From: Principal(m.From.ID), Private: m.Chat.Type == chatPrivate,
		Text: cmp.Or(m.Text, m.Caption), Caption: m.Text == "",
		Forwarded: present(m.ForwardOrigin) || m.IsAutomaticForward})
	t.log.Info("telegram: intake line taken", "update_id", u.UpdateID, "request", id, "outcome", outcome)
	return true
}

// deleteLine deletes the message that held a line. A failure (the bot may
// not delete in this chat, the message is gone) is logged with the Bot API's
// error code and otherwise ignored: the line is out of the inbox already,
// and a later reply or quote shows removedLine in its place.
func (t *Transport) deleteLine(ctx context.Context, updateID int64, m *message) {
	err := t.c.deleteMessage(ctx, m.Chat.ID, m.MessageID)
	if err == nil {
		return
	}
	code := 0
	if ae, ok := errors.AsType[*apiError](err); ok {
		code = ae.Code
	}
	t.log.Warn("telegram: intake line not deleted", "update_id", updateID, "code", code)
}
