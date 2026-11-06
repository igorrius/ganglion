package agent

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// Enqueuer stores messages in the outbox outside a turn's end
// (queue.Outbox): the acknowledgement of a press goes out while the
// pressed action still runs (ruling R40).
type Enqueuer interface {
	Enqueue(ctx context.Context, msgs ...event.Outgoing) error
}

// SentRefs says where a delivered message went (queue.Outbox); "" when it
// was not delivered.
type SentRefs interface {
	SentRef(ctx context.Context, idemKey string) (string, error)
}

// NoRefs knows of no delivered message: expired questions wait for a press.
type NoRefs struct{}

// SentRef implements SentRefs.
func (NoRefs) SentRef(context.Context, string) (string, error) { return "", nil }

// errorPrefix starts the outcome of a call that failed, as the model
// reads it.
const errorPrefix = "error: "

// approvedLine is the last line of an approved question: whether its call
// did what it was asked, from the stored outcome's flag.
func approvedLine(failed bool) texts.Key {
	if failed {
		return texts.LineApprovedFailed
	}
	return texts.LineApproved
}

// stamped is a closed question's line with the time it was decided, in the
// node's zone (R-POL-9); a call answered before times were stored has none.
func (a *Agent) stamped(line string, at time.Time) string {
	if at.IsZero() {
		return line
	}
	return line + ", " + at.In(a.cfg.Location).Format("15:04")
}

// ack is the message that answers the press with text as a toast; false
// when the press has no id to answer (a channel without toasts).
func (t *turn) ack(text string) (event.Outgoing, bool) {
	if t.decision.Query == "" {
		return event.Outgoing{}, false
	}
	ev := t.event
	return event.Outgoing{IdemKey: ev.IdemKey + ":ack", Transport: ev.Source, Conv: ev.Conv, Text: text, Ack: t.decision.Query}, true
}

// toast answers a press that did not take with text, among the turn's
// replies.
func (t *turn) toast(text string) {
	if o, ok := t.ack(text); ok {
		t.out = append(t.out, o)
	}
}

// acknowledge stores, before the pressed action runs, the toast and, for
// a question, the question folded under status in place of its buttons, so the
// owner sees the press taken and cannot press again while it runs
// (ruling R40). They go to the outbox now, not with the turn's replies;
// their keys are the press's, so a rerun stores them once. A failure
// costs only the early feedback: it is logged and the press goes on.
func (a *Agent) acknowledge(ctx context.Context, t *turn, toast texts.Key, question, status string) {
	var msgs []event.Outgoing
	if o, ok := t.ack(a.label(ctx, t, toast)); ok {
		msgs = append(msgs, o)
	}
	if o, ok := t.editOut(":status", pending.QuickEdit{Text: status, Folded: question}); ok && question != "" {
		msgs = append(msgs, o)
	}
	if len(msgs) == 0 {
		return
	}
	if err := a.d.Outbox.Enqueue(ctx, msgs...); err != nil {
		a.log.Warn("agent: press not acknowledged early", "conv", t.event.Conv, "err", err)
	}
}
