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
	// made is what made this turn's messages, for their origin (R-DLV-14); ""
	// is a turn with a person.
	made  event.OriginKind
	admin bool // the sender may use tools
	calls int  // tool calls so far in this turn; the next is calls+1
	// execCalls are the calls a CLI provider ran through Exec; at most
	// MaxSteps a turn.
	execCalls int
	// planCalls are the plan calls of the turn; they have their own ceiling.
	planCalls int
	// execLog are the CLI provider's calls and results for the history,
	// in the API path's form (logExec).
	execLog []provider.Message
	known   urlSet // addresses the sender gave or a search returned
	// decision is the press of a KindDecision event; toolResult the
	// pending call of a KindToolResult event; schedule the job of a
	// KindSchedule event; command the host command of a KindCommandDone
	// event.
	decision   event.Decision
	toolResult event.ToolResult
	command    event.CommandDone
	schedule   event.Schedule
	module     event.Module       // the escalation of a KindModule event
	notify     event.ModuleNotify // the message of a KindModuleNotify event
	groupReq   event.GroupRequest // the request of a KindGroupRequest event
	built      event.ModuleBuilt  // the build of a KindModuleBuilt event
	review     event.MemoryReview // the conversation of a KindMemoryReview event
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
	// deadline is when the turn's clock runs out (R-LIVE-6).
	deadline time.Time
	// jobs is the note of the conversation's background jobs, built when the
	// model is first asked; "" when nothing runs (R-LIVE-9).
	jobs string
	// taken are the events the turn took from behind it; Complete marks them
	// done with it (R-LIVE-1).
	taken []string
	// kept is what stays in the history if a model call fails later: the
	// additions and the earlier answers the person read, in order (R-LIVE-5,
	// R-LIVE-6).
	kept []provider.Message
	// quotes are the earlier answers sent so far (R-LIVE-5).
	quotes int
	// refs are the person's messages this turn answers, its own first, as each
	// came: the reply's ReplyTo moves on, but their reactions must end (R-LIVE-7).
	refs []string
	// endRun ends the running model call for an addition, and runEnded says
	// it was; both are guarded by mu (R-LIVE-3).
	endRun   context.CancelCauseFunc
	runEnded bool
}

// origin is where what the turn stores now comes from (R-DLV-14). A reminder's text
// was set when it was made, in a clean session or after the owner's press (R-POL-6),
// so it is clean; a system job reports on memory outside content may have reached,
// so it is tainted. A reminder's message names its job (ADR-0091). It reads the
// turn's state, so it is called where that may be read: in a stage, or under t.mu
// from a tool a CLI provider runs.
func (t *turn) origin() event.Origin {
	o := event.Origin{Kind: event.OriginTurn, Session: t.state.Session, Tainted: t.state.Tainted}
	switch t.made {
	case event.OriginTask:
		o.Kind = event.OriginTask
	case event.OriginReminder:
		o.Kind, o.Tainted, o.Job = event.OriginReminder, false, t.schedule.Job
	case event.OriginSystem:
		o.Kind, o.Tainted = event.OriginSystem, true
	case event.OriginTurn, event.OriginModule: // a module's message sets its own origin
	}
	return o
}

// stamp gives each message in out with no origin of its own o.
func stamp(out []event.Outgoing, o event.Origin) {
	for i := range out {
		if out[i].Origin == nil {
			of := o
			out[i].Origin = &of
		}
	}
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
		Folded: e.Folded, Buttons: e.Buttons, Edit: t.decision.Ref}, true
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
