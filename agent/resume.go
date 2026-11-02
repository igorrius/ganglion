package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/event"
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
	Decide(ctx context.Context, id string, st pending.Status, result string) error
	Waiting(ctx context.Context, ids []string) ([]string, error)
}

// QuickAction runs a quick-action button, such as Delete on a list entry.
// It returns the new text of the message holding the button; no model
// turn follows. A rerun of its event runs it again with the same button,
// so it must be idempotent by the button.
type QuickAction func(ctx context.Context, b pending.Button) (edit string, err error)

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
	lineApproved    = "Approved."
	lineDeclined    = "Declined."
	lineUsed        = "Already handled."
	lineExpired     = "Expired."
	lineUnavailable = "This action is no longer available."
)

// resume handles a press of a button and the expiry of a pending call,
// and ends the turn; a message goes on to the next stages (ADR-0032).
func (a *Agent) resume(ctx context.Context, t *turn) (bool, error) {
	switch t.event.Kind {
	case event.KindDecision:
		return true, a.pressed(ctx, t)
	case event.KindToolResult:
		return true, a.expired(ctx, t)
	default: // event.KindMessage
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
		t.edit(answered(p.Item.Question, lineUsed))
		return nil
	case errors.Is(err, pending.ErrExpired):
		t.edit(answered(p.Item.Question, lineExpired))
		return nil
	case err != nil:
		return err
	}
	if p.Item.ID == "" {
		return a.quick(ctx, t, p.Button)
	}
	return a.confirmed(ctx, t, p.Item, p.Button.Action)
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
		t.edit(lineUnavailable)
		return nil
	}
	text, err := run(ctx, b)
	if err != nil {
		return fmt.Errorf("agent: quick action %s: %w", b.Action, err)
	}
	t.edit(text)
	return nil
}

// outcome is how a pending call ended, for the model.
type outcome struct {
	text     string
	external bool // outside content: wrapped as data, the session tainted
}

// confirmed runs an approved call or declines a denied one, stores the
// outcome, edits the question and resumes the conversation with it. A
// rerun (the outcome is stored) does not run the call again.
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
	return a.continueWith(ctx, t, it, o)
}

// decide runs or declines the call, through the journal and without the
// policy, and stores the outcome as done.
func (a *Agent) decide(ctx context.Context, t *turn, it pending.Item, action string) (outcome, error) {
	if it.Status == pending.StatusDone { // a rerun: the outcome is stored
		return outcome{text: it.Result, external: action == pending.ActionApprove && a.external(it.Tool, false)}, nil
	}
	var o outcome
	if action == pending.ActionApprove {
		o = a.runApproved(ctx, t, it)
	} else {
		o = a.settle(ctx, t, it, journal.DecisionDenied, resultDeclined)
	}
	if err := a.d.Pending.Decide(ctx, it.ID, pending.StatusDone, o.text); err != nil {
		return outcome{}, fmt.Errorf("agent: store the outcome of %s: %w", it.ID, err)
	}
	return o, nil
}

// runApproved runs the stored call as it was asked, with its original key
// (R-DLV-6), journalled as approved.
func (a *Agent) runApproved(ctx context.Context, t *turn, it pending.Item) outcome {
	c := it.Call()
	c.Admin, c.Tainted = t.admin, t.state.Tainted
	c.KnownURL = knownURLs(t.state.Messages).has
	res, err := a.journalled(journal.DecisionApproved, a.d.Tools.Run)(ctx, c)
	t.out = append(t.out, res.Out...)
	if err != nil {
		return outcome{text: "error: " + err.Error()}
	}
	return outcome{text: res.Text, external: a.external(it.Tool, res.External)}
}

// settle records a call that does not run, with decision d, and returns
// text as its outcome.
func (a *Agent) settle(ctx context.Context, t *turn, it pending.Item, d journal.Decision, text string) outcome {
	c := it.Call()
	c.Admin, c.Tainted = t.admin, t.state.Tainted
	skip := func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{Text: text}, nil }
	_, _ = a.journalled(d, skip)(ctx, c)
	return outcome{text: text}
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
// call whose session has moved on is closed without a model turn.
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
	o := outcome{text: resultExpired}
	if it.Status == pending.StatusExpired {
		o = a.settle(ctx, t, it, journal.DecisionExpired, resultExpired)
		if err := a.d.Pending.Decide(ctx, it.ID, pending.StatusDone, o.text); err != nil {
			return fmt.Errorf("agent: store the outcome of %s: %w", it.ID, err)
		}
	}
	if !slices.Contains(t.state.Pending, it.ID) {
		return nil
	}
	return a.continueWith(ctx, t, it, o)
}

// continueWith resumes the conversation: the call's outcome joins the
// history as a user-role note after the placeholder result the call got
// in its own turn, so no tool message appears without its call, and the
// model answers as it does a message.
func (a *Agent) continueWith(ctx context.Context, t *turn, it pending.Item, o outcome) error {
	t.record.Outcome = turns.OutcomeOK
	t.state.Pending = slices.DeleteFunc(slices.Clone(t.state.Pending), func(id string) bool { return id == it.ID })
	text := o.text
	if o.external {
		t.state.Tainted = true
		text = tool.WrapExternal(it.Tool, text)
	}
	note := provider.Message{Role: provider.RoleUser, Content: confirmationPrefix + it.Tool + ": " + text}
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}
