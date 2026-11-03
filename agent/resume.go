package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

// Confirmations is the store of calls awaiting the owner and of buttons
// (tool/pending.Store).
type Confirmations interface {
	Press(ctx context.Context, buttonID string, by pending.By) (pending.Press, error)
	Get(ctx context.Context, id string) (pending.Item, error)
	Finish(ctx context.Context, id string, o pending.Outcome) error
	Waiting(ctx context.Context, ids []string) ([]string, error)
}

// confirmationPrefix starts the user-role note that brings a confirmed
// call's outcome into the history: "[confirmation] <tool>: <result>".
const confirmationPrefix = "[confirmation] "

// What the model reads as the outcome of a call that did not run.
const (
	resultDeclined = "the owner declined"
	resultExpired  = "the owner did not answer in time; the call did not run"
)

// Lines added to the question when it is answered (or not).
const (
	lineApproved = "Approved."
	lineDeclined = "Declined."
	lineUsed     = "Already handled."
	lineExpired  = "Expired."
)

// resume handles a press of a button and the expiry of a pending call,
// and ends the turn; a message goes on to the next stages (ADR-0032).
func (a *Agent) resume(ctx context.Context, t *turn) (bool, error) {
	switch t.event.Kind {
	case event.KindDecision:
		return true, a.pressed(ctx, t)
	case event.KindToolResult:
		return true, a.expired(ctx, t)
	default: // event.KindMessage, event.KindSchedule
		return false, nil
	}
}

// dropStalePending forgets pending ids whose calls no longer wait: they
// were answered, expired, or belong to a store that lost them.
func (a *Agent) dropStalePending(ctx context.Context, t *turn) (bool, error) {
	if len(t.state.Pending) == 0 {
		return false, nil
	}
	ids, err := a.d.Pending.Waiting(ctx, t.state.Pending)
	if err != nil {
		return false, fmt.Errorf("agent: pending calls: %w", err)
	}
	t.state.Pending = ids
	return false, nil
}

// pressed handles a press: a refused one at most edits the message; a
// quick action edits it; a confirmation runs or declines the call and
// resumes the conversation.
func (a *Agent) pressed(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	p, err := a.d.Pending.Press(ctx, t.decision.Button, pending.By{Principal: t.msg.From, Conv: t.event.Conv, Event: t.event.ID})
	switch {
	case errors.Is(err, pending.ErrNotYours), errors.Is(err, pending.ErrNotFound):
		a.log.Info("agent: press refused", "conv", t.event.Conv, "principal", t.msg.From, "err", err)
		return nil
	case errors.Is(err, pending.ErrUsed):
		t.closeButtons(p.Item.Question, lineUsed)
		return nil
	case errors.Is(err, pending.ErrExpired):
		t.closeButtons(p.Item.Question, lineExpired)
		return nil
	case err != nil:
		return err
	}
	if p.Item.ID == "" {
		return a.quick(ctx, t, p.Button)
	}
	return a.confirmed(ctx, t, p.Item, p.Button.Action)
}

// closeButtons removes the buttons of a message whose press did not take.
// A question gets a line saying why; any other message (a quick action's
// list entry, whose text the agent cannot write again) keeps its text.
func (t *turn) closeButtons(question, line string) {
	if question == "" {
		t.edit("")
		return
	}
	t.edit(answered(question, line))
}

// answered is the question with a line saying how it ended.
func answered(question, line string) string {
	if question == "" {
		return line
	}
	return question + "\n\n" + line
}

// quick runs a quick action and edits its message.
func (a *Agent) quick(ctx context.Context, t *turn, b pending.Button) error {
	run, ok := a.d.QuickActions[b.Action]
	if !ok {
		a.log.Warn("agent: no quick action", "action", b.Action, "conv", t.event.Conv)
		t.closeButtons("", "")
		return nil
	}
	e, err := run(ctx, pending.QuickPress{Button: b, Transport: t.event.Source, Event: t.event.ID})
	if err != nil {
		return fmt.Errorf("agent: quick action %s: %w", b.Action, err)
	}
	t.editWith(e)
	return nil
}

// confirmed runs an approved call or declines a denied one, stores the
// outcome, edits the question and resumes the conversation with it. A
// rerun (the outcome is stored) does not run the call again. A call the
// conversation does not wait for (a quick action asked it, or the
// session moved on) is closed without a model turn.
func (a *Agent) confirmed(ctx context.Context, t *turn, it pending.Item, action string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	o, err := a.decide(ctx, t, it, action)
	if err != nil {
		return err
	}
	line := lineDeclined
	if action == pending.ActionApprove {
		line = lineApproved
	}
	t.edit(answered(it.Question, line))
	t.out = append(t.out, o.Out...) // again on a rerun: the outbox keeps one per key
	if !slices.Contains(t.state.Pending, it.ID) {
		return nil
	}
	return a.continueWith(ctx, t, it, o)
}

// decide runs or declines the call, through the journal and without the
// policy, and stores the whole outcome as done.
func (a *Agent) decide(ctx context.Context, t *turn, it pending.Item, action string) (pending.Outcome, error) {
	if it.Status == pending.StatusDone { // a rerun: the outcome is stored
		return a.stored(it, action == pending.ActionApprove), nil
	}
	var o pending.Outcome
	if action == pending.ActionApprove {
		o = a.runApproved(ctx, t, it)
	} else {
		o = a.settle(ctx, t, it, journal.DecisionDenied, resultDeclined)
	}
	if err := a.d.Pending.Finish(ctx, it.ID, o); err != nil {
		return pending.Outcome{}, fmt.Errorf("agent: store the outcome of %s: %w", it.ID, err)
	}
	return o, nil
}

// stored is the outcome of a done call as the first run stored it; the
// tool's spec still marks outside content when the call ran.
func (a *Agent) stored(it pending.Item, ran bool) pending.Outcome {
	return pending.Outcome{Result: it.Result, External: it.External || ran && a.external(it.Tool, false), Out: it.Out}
}

// runApproved runs the stored call as it was asked, with its original key
// (R-DLV-6), journalled as approved.
func (a *Agent) runApproved(ctx context.Context, t *turn, it pending.Item) pending.Outcome {
	c := it.Call()
	c.Admin, c.Tainted = t.admin, it.Tainted || t.state.Tainted
	c.KnownURL = knownURLs(t.state.Messages).has
	if o, refused := a.refuseBadArgs(ctx, c); refused {
		return o
	}
	res, err := a.journalled(journal.DecisionApproved, a.d.Tools.Run)(ctx, c)
	if err != nil {
		// A failed call can still carry messages, as in runTool.
		return pending.Outcome{Result: "error: " + err.Error(), Out: res.Out}
	}
	return pending.Outcome{Result: res.Text, External: a.external(it.Tool, res.External), Out: res.Out}
}

// settle records a call that does not run, with decision d, and returns
// text as its outcome.
func (a *Agent) settle(ctx context.Context, t *turn, it pending.Item, d journal.Decision, text string) pending.Outcome {
	c := it.Call()
	c.Admin, c.Tainted = t.admin, it.Tainted || t.state.Tainted
	if o, refused := a.refuseBadArgs(ctx, c); refused {
		return o
	}
	skip := func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{Text: text}, nil }
	_, _ = a.journalled(d, skip)(ctx, c)
	return pending.Outcome{Result: text}
}

// refuseBadArgs checks a stored call's arguments against its tool's
// declared parameters, as the policy checks a new call (ruling R18), so
// a call parked before that check existed cannot run or be recorded as
// declined with arguments the policy never read. A refused call is
// journalled forbidden and its outcome is the fixed error.
func (a *Agent) refuseBadArgs(ctx context.Context, c tool.Call) (pending.Outcome, bool) {
	h, ok := a.d.Tools.Lookup(c.Name)
	if !ok {
		return pending.Outcome{}, false // the registry refuses an unknown tool when it runs
	}
	err := tool.CheckArgs(h.Spec(), c.Args)
	if err == nil {
		return pending.Outcome{}, false
	}
	err = fmt.Errorf("agent: %s: %w", c.Name, err)
	refuse := func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, err }
	_, _ = a.journalled(journal.DecisionForbidden, refuse)(ctx, c)
	return pending.Outcome{Result: "error: " + err.Error()}, true
}

// journalled is f behind tracing and the journal, with decision d: the
// chain of a call the owner decided, without the policy.
func (a *Agent) journalled(d journal.Decision, f tool.Func) tool.Func {
	decided := func(ctx context.Context, c tool.Call) (tool.Result, error) {
		journal.Annotate(ctx, d)
		return f(ctx, c)
	}
	return tool.Chain(decided, slices.Concat([]tool.Middleware{a.traced}, a.d.Middleware)...)
}

// external reports whether a result of the tool is outside content: the
// tool says so in its spec or in the result.
func (a *Agent) external(name string, flagged bool) bool {
	h, ok := a.d.Tools.Lookup(name)
	return flagged || ok && h.Spec().Class == tool.ClassExternal
}

// expired resumes the conversation of a call nobody answered in time. A
// call whose session has moved on is closed without a model turn. A call
// that is done already (a rerun, or answered after all) resumes with its
// stored outcome; its messages went out with the turn that stored it.
func (a *Agent) expired(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	it, err := a.d.Pending.Get(ctx, t.toolResult.Pending)
	if errors.Is(err, pending.ErrNotFound) {
		a.log.Warn("agent: expiry of an unknown call", "pending", t.toolResult.Pending, "conv", t.event.Conv)
		return nil
	}
	if err != nil {
		return err
	}
	if it.Status != pending.StatusExpired && it.Status != pending.StatusDone {
		return nil // answered after all
	}
	t.msg.From, t.record.Principal = it.Principal, it.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	o := a.stored(it, false)
	if it.Status == pending.StatusExpired {
		o = a.settle(ctx, t, it, journal.DecisionExpired, resultExpired)
		if err := a.d.Pending.Finish(ctx, it.ID, o); err != nil {
			return fmt.Errorf("agent: store the outcome of %s: %w", it.ID, err)
		}
	}
	if !slices.Contains(t.state.Pending, it.ID) {
		return nil
	}
	t.noTools = true // the model only tells the owner (ruling R33)
	return a.continueWith(ctx, t, it, o)
}

// continueWith resumes the conversation: the call's outcome joins the
// history as a user-role note after the placeholder result the call got
// in its own turn, so no tool message appears without its call, and the
// model answers as it does a message. The outcome is capped like a tool
// result in a transcript (transcript.Cap), on both paths: an approved
// page is not the owner's words and must not fill the prompt.
//
// The resumed turn passes the daily limit as a message does; over it the
// note joins the history without a model turn. Its calls run one level
// deeper than the resumed call (R-AGT-4, ruling R33).
func (a *Agent) continueWith(ctx context.Context, t *turn, it pending.Item, o pending.Outcome) error {
	t.record.Outcome = turns.OutcomeOK
	t.state.Pending = slices.DeleteFunc(slices.Clone(t.state.Pending), func(id string) bool { return id == it.ID })
	text := o.Result
	if o.External {
		t.state.Tainted = true
		text = tool.WrapExternal(it.Tool, text)
	}
	note := provider.Message{Role: provider.RoleUser, Content: confirmationPrefix + it.Tool + ": " + transcript.Cap(text)}
	if over, _ := a.limits(ctx, t); over {
		t.state.Messages, t.state.Last = append(t.state.Messages, note), t.now
		return nil
	}
	t.depth = it.Depth + 1
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}
