package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// Jobs reads scheduled jobs (schedule.Store).
type Jobs interface {
	Get(ctx context.Context, id string) (schedule.Job, error)
}

// reminderPrefix starts the history note of a sent reminder.
const reminderPrefix = "[reminder] sent to this chat as scheduled: "

// taskSpeaker is who says a task's instruction in its history.
const taskSpeaker = "Scheduled task"

// The line a task run's last user message gets (ruling R41): a task told
// to "send" its findings must not look for a tool to send them with.
const (
	taskLine        = "[unattended scheduled run: your final answer is sent to %s automatically as the message; no tool is needed to send it; write it in the language of the instruction and translate what you read, keeping no words of another language]"
	recipientAnyone = "the owner"
	maxNameRunes    = 64
)

// scheduled handles the time of a scheduled job (design section 5.3).
// Only the scheduler's events count; a schedule event from any other
// source does nothing. The job is read now, so one deleted after its
// event was put does nothing, and an event that does not fit its job is
// dropped. A reminder is sent as it is, with no model and no tools (a
// recurring one with its Done button), leaves a note in the chat's history
// and ends the turn. A task goes on as a turn of its own conversation: the
// owner is the principal (an admin check at run time decides the tools), the
// fixed instruction is the message, the grant is on every call, and the
// answer goes to the grant's reply target. A system job runs its registered code, without
// a model, and ends the turn; so does a module job, through ModuleCron.
func (a *Agent) scheduled(ctx context.Context, t *turn) (bool, error) {
	if t.event.Kind != event.KindSchedule {
		return false, nil
	}
	t.record.Outcome = turns.OutcomeCommand
	if t.event.Source != schedule.Source {
		a.log.Warn("agent: schedule event from another source", "source", t.event.Source, "conv", t.event.Conv)
		return true, nil
	}
	j, err := a.d.Jobs.Get(ctx, t.schedule.Job)
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		a.log.Info("agent: scheduled event of an unknown job", "job", t.schedule.Job, "conv", t.event.Conv)
		return true, nil
	case err != nil:
		return false, fmt.Errorf("agent: job %s: %w", t.schedule.Job, err)
	case j.Deleted || j.EventConv() != t.event.Conv:
		a.log.Info("agent: scheduled event skipped", "job", j.ID, "deleted", j.Deleted, "conv", t.event.Conv)
		return true, nil
	}
	t.msg = event.Message{From: j.Owner, Name: taskSpeaker, Text: j.Text}
	t.record.Principal, t.transport = j.Owner, j.Transport
	switch j.Kind {
	case schedule.KindReminder:
		t.made, t.replyConv = event.OriginReminder, j.Conv
		if err := a.sendReminder(ctx, t, j); err != nil {
			return false, err
		}
		return true, a.noteReminder(ctx, t, j)
	case schedule.KindTask:
		t.record.Outcome = turns.OutcomeOK
		return false, a.asTask(ctx, t, j)
	case schedule.KindSystem:
		t.made, t.replyConv = event.OriginSystem, j.Conv
		return true, a.system(ctx, t, j)
	case schedule.KindModule:
		a.moduleCron(ctx, j, t.event.IdemKey)
		return true, nil
	default:
		a.log.Warn("agent: scheduled job of an unknown kind", "job", j.ID, "kind", j.Kind)
		return true, nil
	}
}

// asTask makes t a turn of task j: its owner as the principal, its transport,
// its grant on every call and its reply target, and the name of who receives it.
func (a *Agent) asTask(ctx context.Context, t *turn, j schedule.Job) error {
	t.msg = event.Message{From: j.Owner, Name: taskSpeaker, Text: j.Text}
	t.record.Principal, t.transport, t.made = j.Owner, j.Transport, event.OriginTask
	g := j.Grant
	t.grant, t.replyConv = &g, g.ReplyTo
	return a.setRecipient(ctx, t, j.Owner)
}

// sendReminder adds reminder j's message to the turn, with the buttons it
// carries in its owner's language (ADR-0091). The buttons are stored before
// the message is, so a rerun of the event stores another set and the message,
// keyed once, shows one of them.
func (a *Agent) sendReminder(ctx context.Context, t *turn, j schedule.Job) error {
	buttons, err := a.d.ReminderButtons(ctx, a.speaker(ctx, t), j)
	if err != nil {
		return fmt.Errorf("agent: buttons of reminder %s: %w", j.ID, err)
	}
	t.reply(j.Text)
	t.out[len(t.out)-1].Buttons = buttons
	return nil
}

// noteReminder adds a "[reminder]" note of the sent reminder to the
// chat's history, so the next turn knows it went out (ruling R50: the
// live run of 2026-11-03 said a reminder "may not have fired" minutes
// after it was sent). A session silent for longer than the idle time is
// archived first, so a daily reminder does not keep an old session open.
func (a *Agent) noteReminder(ctx context.Context, t *turn, j schedule.Job) error {
	if _, err := a.idle(ctx, t); err != nil {
		return err
	}
	t.note(reminderPrefix + transcript.Cap(j.Text))
	return nil
}

// system runs a system job's code for its owner, if the owner is still
// an admin, and sends the report to the job's conversation (ruling R36).
// A job whose code the node does not have does nothing.
func (a *Agent) system(ctx context.Context, t *turn, j schedule.Job) error {
	run, ok := a.d.SystemJobs[j.ID]
	if !ok {
		a.log.Warn("agent: no code for a system job", "job", j.ID)
		return nil
	}
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: system job of a principal who is not an admin; skipped", "job", j.ID, "principal", j.Owner)
		return nil
	}
	report, err := run(ctx, a.speaker(ctx, t))
	if err != nil {
		return fmt.Errorf("agent: system job %s: %w", j.ID, err)
	}
	t.reply(report)
	return nil
}

// moduleCron runs a module's cron job: the module's host does the work,
// without a model and without a message (R-RUN-12). The run is the
// schedule event's idempotency key, the slot (ruling 3 of M4c). A failure
// of the node is logged and not tried again: the next slot runs the module
// anyway, and a retried event would run it twice.
func (a *Agent) moduleCron(ctx context.Context, j schedule.Job, run string) {
	if err := a.d.ModuleCron(ctx, j.Text, run); err != nil {
		a.log.Warn("agent: module cron run failed", "job", j.ID, "module", j.Text, "err", err)
	}
}

// grantCommandsLine tells a task run the exact commands its grant lists, so
// the model repeats them as approved: a command that needs a press runs in a
// task only with its argv and folder exact (R-POL-8).
const grantCommandsLine = "[the commands this task's grant lists, which host.exec runs only with exactly this argv and dir: %s]"

// grantCommands is the grant line of a task run, or "" when its grant lists
// no command: each command as hostcmd.Show writes it, in its folder, home for
// one without dir, each in a code span a backtick cannot end.
func grantCommands(g *tool.Grant, home string) string {
	if g == nil || len(g.Commands) == 0 {
		return ""
	}
	cmds := make([]string, len(g.Commands))
	for i, c := range g.Commands {
		dir := c.Dir
		if dir == "" {
			dir = home
		}
		cmds[i] = text.CodeSpan(hostcmd.Show(c.Argv)) + " in " + text.CodeSpan(hostcmd.Show([]string{dir}))
	}
	return fmt.Sprintf(grantCommandsLine, strings.Join(cmds, "; "))
}

// setRecipient names the owner who receives a task run's answer, on one
// line and short, or "the owner" when the access list has no name.
func (a *Agent) setRecipient(ctx context.Context, t *turn, owner string) error {
	name, err := a.d.Names(ctx, owner)
	if err != nil {
		return fmt.Errorf("agent: name of %s: %w", owner, err)
	}
	t.recipient = text.Truncate(strings.Join(strings.Fields(name), " "), maxNameRunes)
	if t.recipient == "" {
		t.recipient = recipientAnyone
	}
	return nil
}
