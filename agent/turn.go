package agent

import (
	"context"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

// turn is everything one handled event needs and produces.
type turn struct {
	// mu guards the turn while a CLI provider runs tools from its own
	// goroutines (agent.useTool); stages run one at a time and do not
	// take it.
	mu     sync.Mutex
	event  event.Event
	msg    event.Message
	state  State
	now    time.Time
	record turns.Turn // the trace: route, tools, tokens, outcome
	out    []event.Outgoing
	admin  bool // the sender may use tools
	calls  int  // tool calls so far in this turn; the next is calls+1
	// execCalls are the calls a CLI provider ran through Exec; at most
	// MaxSteps a turn.
	execCalls int
	// execLog are the CLI provider's calls and results for the history,
	// in the API path's form (logExec).
	execLog []provider.Message
	known   urlSet // addresses the sender gave or a search returned
	// decision is the press of a KindDecision event; toolResult the
	// pending call of a KindToolResult event; schedule the job of a
	// KindSchedule event.
	decision   event.Decision
	toolResult event.ToolResult
	schedule   event.Schedule
	// transport and replyConv are where the answer goes: the event's
	// source and conversation, or for a scheduled job its owner's
	// transport and the job's reply target.
	transport, replyConv string
	grant                *tool.Grant // a scheduled task's grant, on every call of its turn
	// recipient is who receives a scheduled task's answer, by name, for
	// the model's line about it (ruling R41); "" outside a task.
	recipient string
	// depth is the depth of the turn's calls (R-AGT-4): 0 in a turn a
	// person or a job started, the resumed call's depth plus one in a
	// turn a confirmation resumed (ruling R33).
	depth int
	// noTools offers the model no tools: an expiry only tells the owner
	// (ruling R33).
	noTools bool
}

// reply adds the answer to the person's message. A turn answers once, so
// the key is fixed and a rerun turn's answer is sent once (R-DLV-6).
func (t *turn) reply(text string) {
	t.out = append(t.out, event.Outgoing{IdemKey: t.event.IdemKey + ":reply:0", Transport: t.transport, Conv: t.replyConv, Text: text, ReplyTo: t.msg.Ref})
}

// edit changes the message holding the pressed button to text and removes
// its buttons; "" keeps the text and removes only the buttons.
func (t *turn) edit(text string) { t.editWith(pending.QuickEdit{Text: text}) }

// editWith changes the message holding the pressed button to e's text and
// buttons. A turn edits once, so the key is fixed.
func (t *turn) editWith(e pending.QuickEdit) {
	if o, ok := t.editOut(":edit", e); ok {
		t.out = append(t.out, o)
	}
}

// editOut is the edit of the message holding the pressed button, keyed by
// the press and suffix; false when the press names no message.
func (t *turn) editOut(suffix string, e pending.QuickEdit) (event.Outgoing, bool) {
	if t.decision.Ref == "" {
		return event.Outgoing{}, false
	}
	ev := t.event
	key := ev.IdemKey + suffix
	if e.Key != "" {
		key = e.Key
	}
	return event.Outgoing{IdemKey: key, Transport: ev.Source, Conv: ev.Conv, Text: e.Text,
		Buttons: e.Buttons, Edit: t.decision.Ref}, true
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
