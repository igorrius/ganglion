package telegram

import (
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

// intercept takes an update that is one intake line out of the inbox path
// and reports whether it did. Only the text of a message from a person is
// read: a caption that holds a line is not taken, for the page shows the
// line as text to copy and a caption is a file's, which the inbox
// describes. The line goes to the intake func with the sender and whether
// the chat is private (the intake decides who may), then the message is
// deleted, in a group too when the bot can (R-INT-5). The update counts as
// handled and the sink never sees it. The text is never logged: the Info
// line holds the update id, the request id and the outcome.
func (t *Transport) intercept(ctx context.Context, u update) bool {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || !intake.IsLine(m.Text) {
		return false
	}
	id, outcome := t.intake(ctx, intake.Pasted{From: Principal(m.From.ID), Private: m.Chat.Type == chatPrivate, Text: m.Text})
	t.deleteLine(ctx, u.UpdateID, id, m)
	t.log.Info("telegram: intake line taken", "update_id", u.UpdateID, "request", id, "outcome", outcome)
	return true
}

// deleteLine deletes the message that held a line. A failure (the bot may
// not delete in this chat, the message is gone) is logged with the Bot API's
// error code and otherwise ignored: the line is out of the inbox already.
func (t *Transport) deleteLine(ctx context.Context, updateID int64, request string, m *message) {
	err := t.c.deleteMessage(ctx, m.Chat.ID, m.MessageID)
	if err == nil {
		return
	}
	code := 0
	if ae, ok := errors.AsType[*apiError](err); ok {
		code = ae.Code
	}
	t.log.Warn("telegram: intake line not deleted", "update_id", updateID, "request", request, "code", code)
}
