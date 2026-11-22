package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/texts"
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
	Replace(ctx context.Context, id string) ([]pending.Item, error)
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// confirmationPrefix starts the user-role note that brings a confirmed
// call's outcome into the history: "[confirmation] <tool>: <result>".
const confirmationPrefix = "[confirmation] "

// buttonPrefix starts the user-role note of a quick action that changed
// something: "[button] <what the owner did>" (ruling R42).
const buttonPrefix = "[button] "

// What the model reads as the outcome of a call that did not run.
const (
	resultDeclined = "the owner declined"
	resultExpired  = "the owner did not answer in time; the call did not run"
)

// Lines that say how a question ended, above its folded text (R-POL-9).
const ()

// resume handles a press of a button, the expiry of a pending call, the
// end of a host command or a module build in the background, a module's
// escalation, a module's message and a group's request, and ends the turn; a message goes on to the next
// stages (ADR-0032, R-CMD-4).
func (a *Agent) resume(ctx context.Context, t *turn) (bool, error) {
	switch t.event.Kind {
	case event.KindDecision:
		return true, a.pressed(ctx, t)
	case event.KindToolResult:
		return true, a.expired(ctx, t)
	case event.KindCommandDone:
		return true, a.commandDone(ctx, t)
	case event.KindModuleBuilt:
		return true, a.moduleBuilt(ctx, t)
	case event.KindMemoryReview:
		return true, a.memoryReview(ctx, t)
	case event.KindModule:
		return true, a.moduleEvent(ctx, t)
	case event.KindModuleNotify:
		return true, a.moduleNotifyEvent(ctx, t)
	case event.KindMessage, event.KindSchedule:
	case event.KindModuleTrigger: // never here: Handle runs a trigger before begin
	case event.KindGroupRequest:
		return true, a.groupRequest(ctx, t)
	case event.KindSecretClosed:
		return true, a.secretClosed(ctx, t)
	case event.KindGroupJoined: // never here: the access filter takes it
	}
	return false, nil
}

// dropStale forgets pending ids whose calls no longer wait (they were
// answered, expired, or belong to a store that lost them) and commands
// whose end is not to be told any more.
func (a *Agent) dropStale(ctx context.Context, t *turn) (bool, error) {
	if len(t.state.Pending) > 0 {
		ids, err := a.d.Pending.Waiting(ctx, t.state.Pending)
		if err != nil {
			return false, fmt.Errorf("agent: pending calls: %w", err)
		}
		t.state.Pending = ids
	}
	return false, a.dropStaleCommands(ctx, t)
}

// pressed handles a press: a refused one gets a toast saying why and at
// most an edit of the message; a quick action edits it; a confirmation
// runs or declines the call and resumes the conversation. A valid press
// is acknowledged before its action runs (ruling R40).
func (a *Agent) pressed(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	p, err := a.d.Pending.Press(ctx, t.decision.Button, pending.By{Principal: t.msg.From, Conv: t.event.Conv, Event: t.event.ID})
	switch {
	case errors.Is(err, pending.ErrNotYours):
		a.log.Info("agent: press refused", "conv", t.event.Conv, "principal", t.msg.From, "err", err)
		t.toast(a.label(ctx, t, texts.ToastNotYours))
		return nil
	case errors.Is(err, pending.ErrNotFound):
		a.log.Info("agent: press refused", "conv", t.event.Conv, "principal", t.msg.From, "err", err)
		t.toast(a.label(ctx, t, texts.ToastNoButton))
		return nil
	case errors.Is(err, pending.ErrUsed) && p.Item.ID == "":
		t.toast(a.label(ctx, t, texts.ToastUsed))
		return nil // a quick button tapped twice: the first tap's edit stands
	case errors.Is(err, pending.ErrUsed):
		t.toast(a.label(ctx, t, texts.ToastUsed))
		t.closeButtons(p.Item.Question, a.usedLine(ctx, t, p))
		return nil
	case errors.Is(err, pending.ErrExpired):
		t.toast(a.label(ctx, t, texts.ToastExpired))
		t.closeButtons(p.Item.Question, a.stamped(a.say(ctx, t, texts.LineExpired, nil), p.Item.Expires))
		return nil
	case errors.Is(err, pending.ErrReplaced):
		t.toast(a.label(ctx, t, texts.ToastReplaced))
		t.closeButtons(p.Item.Question, a.stamped(a.say(ctx, t, texts.LineReplaced, nil), p.Item.Answered))
		return nil
	case err != nil:
		return err
	}
	if p.Item.ID == "" {
		return a.quick(ctx, t, p.Button)
	}
	return a.confirmed(ctx, t, p.Item, p.Button.Action)
}

// usedLine is how an answered question ended, for a second press of it:
// the answer a press gave, or that it expired. The time is the decision's,
// not this press's.
func (a *Agent) usedLine(ctx context.Context, t *turn, p pending.Press) string {
	switch {
	case p.Answer == pending.ActionApprove:
		return a.stamped(a.say(ctx, t, approvedLine(p.Item.Failed), nil), p.Item.Answered)
	case p.Answer == pending.ActionDeny:
		return a.stamped(a.say(ctx, t, texts.LineDeclined, nil), p.Item.Answered)
	case p.Item.Status == pending.StatusExpired || p.Item.Status == pending.StatusDone:
		return a.stamped(a.say(ctx, t, texts.LineExpired, nil), p.Item.Expires)
	default:
		return a.say(ctx, t, texts.LineUsed, nil)
	}
}

// closeButtons removes the buttons of a message whose press did not take.
// A question folds under a line saying why; any other message (a quick
// action's list entry, whose text the agent cannot write again) keeps its text.
func (t *turn) closeButtons(question, line string) {
	if question == "" {
		t.edit("")
		return
	}
	t.editWith(pending.QuickEdit{Text: line, Folded: question})
}

// quick runs a quick action and edits its message; what it changed joins
// the history as a note (ruling R42). The presser must still be an
// admin: the button was made for one, who may have lost the role since.
func (a *Agent) quick(ctx context.Context, t *turn, b pending.Button) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Info("agent: quick action refused: not an admin", "action", b.Action, "principal", t.msg.From)
		t.toast(a.label(ctx, t, texts.ToastNotAllowed))
		return nil
	}
	run, ok := a.d.QuickActions[b.Action]
	if !ok {
		a.log.Warn("agent: no quick action", "action", b.Action, "conv", t.event.Conv)
		t.toast(a.label(ctx, t, texts.ToastNoButton))
		t.closeButtons("", "")
		return nil
	}
	a.acknowledge(ctx, t, texts.ToastAccepted, "", "")
	a.hideButtons(ctx, t)
	e, err := run(ctx, pending.QuickPress{Speak: a.speaker(ctx, t), Button: b, Transport: t.event.Source, Event: t.event.ID})
	if err != nil {
		return fmt.Errorf("agent: quick action %s: %w", b.Action, err)
	}
	t.editWith(e)
	if e.Note != "" {
		t.note(buttonPrefix + transcript.Cap(e.Note))
	}
	if e.Reviewed {
		t.state.Reviewed = len(t.state.Messages)
	}
	return nil
}

// note adds a user-role note to the session's history without a model
// turn, opening a session when there is none; the next turn reads it.
func (t *turn) note(text string) {
	if t.state.Session == "" {
		t.state.Session, t.state.Started = t.event.ID, t.now
	}
	t.state.Messages = append(t.state.Messages, provider.Message{Role: provider.RoleUser, Content: text})
	t.state.Last = t.now
}

// noteQuiet adds a note as note does but leaves Last as it was, unless the
// note opens the session: a module's note is no exchange with the person,
// so it must not keep the session, and its taint, alive past SessionIdle
// (R-RUN-16).
func (t *turn) noteQuiet(text string) {
	last, opens := t.state.Last, t.state.Session == ""
	t.note(text)
	if !opens {
		t.state.Last = last
	}
}

// confirmed acknowledges the press, runs an approved call or declines a
// denied one, stores the outcome, folds the question and resumes the
// conversation with it. An approved plan's question is not folded: its run
// made it the plan's message (Spec.KeepsQuestion). A
// rerun (the outcome is stored) does not run the call again. A call the
// conversation does not wait for (a quick action asked it, or the
// session moved on) is closed without a model turn.
func (a *Agent) confirmed(ctx context.Context, t *turn, it pending.Item, action string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	keeps := action == pending.ActionApprove && a.keepsQuestion(it.Tool)
	switch {
	case keeps: // the approved run's own edit follows at once
		a.acknowledge(ctx, t, texts.ToastApproved, "", "")
	case action == pending.ActionApprove:
		a.acknowledge(ctx, t, texts.ToastApproved, it.Question, a.say(ctx, t, texts.LineRunning, nil))
	default:
		a.acknowledge(ctx, t, texts.ToastDeclined, it.Question, a.stamped(a.say(ctx, t, texts.LineDeclined, nil), it.Answered))
	}
	o, err := a.decide(ctx, t, it, action)
	if err != nil {
		return err
	}
	line := texts.LineDeclined
	if action == pending.ActionApprove {
		line = approvedLine(o.Failed)
	}
	// The call's messages, then the question with how it ended (ruling
	// R40); again on a rerun: the outbox keeps one per key. A question the
	// approved run made its own message stays as that run left it, unless the
	// run failed and left the buttons.
	t.out = append(t.out, o.Out...)
	if !keeps || o.Failed {
		t.editWith(pending.QuickEdit{Text: a.stamped(a.say(ctx, t, line, nil), it.Answered), Folded: it.Question})
	}
	if !slices.Contains(t.state.Pending, it.ID) {
		return nil
	}
	return a.continueWith(ctx, t, it, o)
}

// keepsQuestion reports whether the tool's approved run makes its question
// its own message (a plan, ruling 8 of the M3d commands plan): it is not
// folded. An unknown tool folds as any.
func (a *Agent) keepsQuestion(name string) bool {
	h, ok := a.d.Tools.Lookup(name)
	return ok && h.Spec().KeepsQuestion
}

// decide runs or declines the call, through the journal and without the
// policy, stores the whole outcome as done, and notes the call on the turn.
func (a *Agent) decide(ctx context.Context, t *turn, it pending.Item, action string) (pending.Outcome, error) {
	note := turns.NoteDeclined
	if action == pending.ActionApprove {
		note = turns.NoteApproved
	}
	if it.Status == pending.StatusDone { // a rerun: the outcome is stored, its time is not
		o := a.stored(it, action == pending.ActionApprove)
		t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: it.Tool, OK: !o.Failed, Note: note})
		return o, nil
	}
	started := a.d.Now()
	var o pending.Outcome
	if action == pending.ActionApprove {
		o = a.runApproved(ctx, t, it)
	} else {
		o = a.settle(ctx, t, it, journal.DecisionDenied, resultDeclined)
	}
	if err := a.d.Pending.Finish(ctx, it.ID, o); err != nil {
		return pending.Outcome{}, fmt.Errorf("agent: store the outcome of %s: %w", it.ID, err)
	}
	t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: it.Tool, OK: !o.Failed, Ms: a.d.Now().Sub(started).Milliseconds(), Note: note})
	return o, nil
}

// stored is the outcome of a done call as the first run stored it; the
// tool's spec still marks outside content when the call ran.
func (a *Agent) stored(it pending.Item, ran bool) pending.Outcome {
	return pending.Outcome{Result: it.Result, External: it.External || ran && a.external(it.Tool, false), Failed: it.Failed, Out: it.Out,
		Job: it.Job}
}

// runApproved runs the stored call as it was asked, with its original key
// (R-DLV-6), journalled as approved.
func (a *Agent) runApproved(ctx context.Context, t *turn, it pending.Item) pending.Outcome {
	c := it.Call()
	c.Admin, c.Tainted = t.admin, it.Tainted || t.state.Tainted
	// The owner pressed Approve on this call: a tool that never runs
	// unapproved reads it (ADR-0040). The approved plan is asked too, so a
	// pressed plan keeps what it still lists.
	c.Speak, c.Approved, c.Planned = a.speaker(ctx, t), true, a.planned(ctx, it.Conv)
	c.KnownURL, c.On, c.TurnedOn = knownURLs(t.state.Messages).has, t.state.Tools, t.state.Tools
	if o, refused := a.refuseBadArgs(ctx, c); refused {
		return o
	}
	res, err := a.journalled(journal.DecisionApproved, a.d.Tools.Run)(ctx, c)
	if err != nil {
		// A failed call can still carry messages, as in runTool.
		return pending.Outcome{Result: errorPrefix + err.Error(), Failed: true, Out: res.Out}
	}
	return pending.Outcome{Result: res.Text, External: a.external(it.Tool, res.External), Out: res.Out, Job: res.Job}
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
	return pending.Outcome{Result: errorPrefix + err.Error(), Failed: true}, true
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
	if o.Result == resultExpired {
		a.closeExpired(ctx, t, it)
		t.record.Tools = append(t.record.Tools, turns.ToolUse{Name: it.Tool, Note: turns.NoteExpired})
	}
	if !slices.Contains(t.state.Pending, it.ID) {
		return nil
	}
	t.noTools = true // the model only tells the owner (ruling R33)
	return a.continueWith(ctx, t, it, o)
}

// closeExpired takes the buttons off the question of an expired call and
// folds it under a line saying so. A question that was not delivered has nothing to close,
// and a failed look only leaves the buttons, which a late press still closes.
// Its key is the expiry event's, so a rerun stores the edit once.
func (a *Agent) closeExpired(ctx context.Context, t *turn, it pending.Item) {
	ref, err := a.d.Sent.SentRef(ctx, it.Call().Key()+pending.QuestionKeySuffix)
	if err != nil {
		a.log.Warn("agent: the question of an expired call", "pending", it.ID, "err", err)
		return
	}
	if ref == "" {
		return
	}
	t.out = append(t.out, event.Outgoing{IdemKey: t.event.IdemKey + ":edit", Transport: it.Transport, Conv: it.Conv,
		Text: a.stamped(a.say(ctx, t, texts.LineExpired, nil), it.Expires), Folded: it.Question, Edit: ref})
}

// closeReplaced takes the buttons off the question of a call that a newer
// question replaced and folds it under a line saying so (ADR-0088). A
// question that was not delivered has nothing to close. Its key is the
// replaced call's, so a rerun stores the edit once.
func (a *Agent) closeReplaced(ctx context.Context, t *turn, it pending.Item) {
	ref, err := a.d.Sent.SentRef(ctx, it.Call().Key()+pending.QuestionKeySuffix)
	if err != nil {
		a.log.Warn("agent: the question of a replaced call", "pending", it.ID, "err", err)
		return
	}
	if ref == "" {
		return
	}
	t.out = append(t.out, event.Outgoing{IdemKey: it.Call().Key() + ":replaced", Transport: it.Transport, Conv: it.Conv,
		Text: a.stamped(a.say(ctx, t, texts.LineReplaced, nil), it.Answered), Folded: it.Question, Edit: ref})
}

// continueWith resumes the conversation: the call's outcome joins the
// history as a user-role note after the placeholder result the call got
// in its own turn, so no tool message appears without its call, and the
// model answers as it does a message. The outcome is capped like a tool
// result in a transcript (transcript.Cap), on both paths: an approved
// page is not the owner's words and must not fill the prompt.
//
// The resumed turn passes the daily limit as a message does; over it the
// note joins the history without a model turn. Its calls run at the depth
// of the resumed call: a press is the owner's answer, not the agent calling
// itself, so a chain of presses never deepens (R-AGT-4, ADR-0111). A call that left a
// host command in the background lists it, once, so its end resumes the
// conversation in turn (R-CMD-4).
func (a *Agent) continueWith(ctx context.Context, t *turn, it pending.Item, o pending.Outcome) error {
	t.record.Outcome = turns.OutcomeOK
	t.state.Pending = slices.DeleteFunc(slices.Clone(t.state.Pending), func(id string) bool { return id == it.ID })
	if o.Job != "" && !slices.Contains(t.state.Commands, o.Job) {
		t.state.Commands = append(slices.Clone(t.state.Commands), o.Job)
	}
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
	t.depth = it.Depth
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}
