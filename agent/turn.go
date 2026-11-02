package agent

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
)

// turn is everything one handled event needs and produces.
type turn struct {
	event  event.Event
	msg    event.Message
	state  State
	now    time.Time
	record turns.Turn // the trace: route, tools, tokens, outcome
	out    []event.Outgoing
	admin  bool   // the sender may use tools
	calls  int    // tool calls so far in this turn; the next is calls+1
	known  urlSet // addresses the sender gave or a search returned
	// decision is the press of a KindDecision event; toolResult the
	// pending call of a KindToolResult event.
	decision   event.Decision
	toolResult event.ToolResult
}

// reply adds the answer to the person's message. A turn answers once, so
// the key is fixed and a rerun turn's answer is sent once (R-DLV-6).
func (t *turn) reply(text string) {
	e := t.event
	t.out = append(t.out, event.Outgoing{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv, Text: text, ReplyTo: t.msg.Ref})
}

// edit changes the message holding the pressed button to text and removes
// its buttons. A turn edits once, so the key is fixed.
func (t *turn) edit(text string) {
	if t.decision.Ref == "" {
		return
	}
	e := t.event
	t.out = append(t.out, event.Outgoing{IdemKey: e.IdemKey + ":edit", Transport: e.Source, Conv: e.Conv, Text: text, Edit: t.decision.Ref})
}

// stage does one step of a turn; done ends the turn early. An error
// leaves the event to be handled again.
type stage func(ctx context.Context, t *turn) (done bool, err error)

// runStages runs stages in order until one ends the turn or fails.
func runStages(ctx context.Context, t *turn, stages []stage) error {
	for _, s := range stages {
		done, err := s(ctx, t)
		if err != nil || done {
			return err
		}
	}
	return nil
}

// addUsage counts a successful call's tokens and cost in the trace.
func addUsage(r *turns.Turn, c router.Candidate, u provider.Usage) {
	r.InputTokens += u.InputTokens
	r.CacheReadTokens += u.CacheReadTokens
	r.CacheWriteTokens += u.CacheWriteTokens
	r.OutputTokens += u.OutputTokens
	r.CostUSD += c.Cost(u.InputTokens, u.OutputTokens)
}
