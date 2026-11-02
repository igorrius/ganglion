package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
)

// Texts of the quick actions.
const (
	textGone      = "This job no longer exists."
	textDeleted   = "Deleted."
	textSystem    = "A system job cannot be deleted."
	textNotTask   = "Only a task can run now."
	textThisChat  = "this chat"
	textNone      = "none"
	systemMarker  = " (system)"
	labelDetails  = "Details"
	labelDelete   = "Delete"
	labelRunNow   = "Run now"
	textStartedTo = "Started; the answer goes to "
)

// buttonLabels are the labels of the quick actions.
var buttonLabels = map[string]string{ActionDetails: labelDetails, ActionDelete: labelDelete, ActionRunNow: labelRunNow}

// QuickActions are the buttons of a listed job by action name, for the
// agent (agent.Deps.QuickActions). Each edits the job's own message.
func (s Scheduling) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionDetails: s.details, ActionDelete: s.deleteByButton, ActionRunNow: s.runByButton}
}

// buttons stores the quick-action buttons of job j for principal in conv
// and returns them, in the order of actions: Delete only for a job that
// is not a system job, Run now only for a task.
func (s Scheduling) buttons(ctx context.Context, j schedule.Job, principal, conv string, actions ...string) ([]event.Button, error) {
	args, err := json.Marshal(idArgs{ID: j.ID})
	if err != nil {
		return nil, err
	}
	var stored []pending.Button
	for _, action := range actions {
		if action == ActionDelete && j.System || action == ActionRunNow && j.Kind != schedule.KindTask {
			continue
		}
		stored = append(stored, pending.Button{ID: pending.NewButtonID(), Action: action, Label: buttonLabels[action],
			Principal: principal, Conv: conv, Args: args, Expires: s.Now().Add(listButtonsTTL)})
	}
	if err := s.Buttons.AddButtons(ctx, stored...); err != nil {
		return nil, err
	}
	out := make([]event.Button, len(stored))
	for i, b := range stored {
		out[i] = event.Button{ID: b.ID, Label: b.Label}
	}
	return out, nil
}

// pressedJob is the job a quick-action button names, when it still
// exists and belongs to the person who pressed.
func (s Scheduling) pressedJob(ctx context.Context, p pending.QuickPress) (schedule.Job, bool, error) {
	var a idArgs
	if err := json.Unmarshal(p.Button.Args, &a); err != nil {
		return schedule.Job{}, false, fmt.Errorf("schedule: button %s: arguments: %w", p.Button.ID, err)
	}
	j, err := s.Jobs.Get(ctx, a.ID)
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		return schedule.Job{}, false, nil
	case err != nil:
		return schedule.Job{}, false, err
	}
	return j, !j.Deleted && j.Owner == p.Button.Principal, nil
}

// details shows all of a job, with Delete and Run now under it.
func (s Scheduling) details(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	j, ok, err := s.pressedJob(ctx, p)
	if err != nil || !ok {
		return pending.QuickEdit{Text: textGone}, err
	}
	buttons, err := s.buttons(ctx, j, p.Button.Principal, p.Button.Conv, ActionDelete, ActionRunNow)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: s.detailsText(j, p.Button.Conv), Buttons: buttons}, nil
}

// deleteByButton deletes the job at once: the press is the owner's own
// decision, so there is no second question.
func (s Scheduling) deleteByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	j, ok, err := s.pressedJob(ctx, p)
	if err != nil || !ok {
		return pending.QuickEdit{Text: textGone}, err
	}
	err = s.Jobs.Delete(ctx, j.ID, p.Button.Principal)
	switch {
	case errors.Is(err, schedule.ErrSystem):
		return pending.QuickEdit{Text: s.entry(j) + "\n\n" + textSystem}, nil
	case errors.Is(err, schedule.ErrNotFound):
		return pending.QuickEdit{Text: textGone}, nil
	case err != nil:
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: s.entry(j) + "\n\n" + textDeleted}, nil
}

// runByButton runs a task now. A task whose grant may act on the world
// (a side-effect tool, or one no longer registered) asks first, in place
// of the job's message; one that only reads runs at once, keyed by the
// press's event, so handling the press again starts the same run.
func (s Scheduling) runByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	j, ok, err := s.pressedJob(ctx, p)
	switch {
	case err != nil || !ok:
		return pending.QuickEdit{Text: textGone}, err
	case j.Kind != schedule.KindTask:
		return pending.QuickEdit{Text: s.entry(j) + "\n\n" + textNotTask}, nil
	case s.mayAct(j):
		return s.askRunNow(ctx, p, j)
	}
	if err := s.Runner.RunKey(ctx, j.ID, p.Button.Principal, p.Event); err != nil {
		return pending.QuickEdit{}, err
	}
	buttons, err := s.buttons(ctx, j, p.Button.Principal, p.Button.Conv, ActionDetails, ActionDelete, ActionRunNow)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	text := s.entry(j) + "\n\n" + textStartedTo + replyTarget(j.Grant.ReplyTo, p.Button.Conv) + "."
	return pending.QuickEdit{Text: text, Buttons: buttons}, nil
}

// mayAct reports whether a task's grant holds a tool with a side effect;
// a tool that is no longer registered counts as one.
func (s Scheduling) mayAct(j schedule.Job) bool {
	for _, name := range j.Grant.Tools {
		if spec, ok := s.Specs(name); !ok || spec.Class == tool.ClassSideEffect {
			return true
		}
	}
	return false
}

// askRunNow parks a schedule.run_now call for the owner and shows the
// question with its buttons in the job's message. The button is the
// call's turn, so a rerun asks once.
func (s Scheduling) askRunNow(ctx context.Context, p pending.QuickPress, j schedule.Job) (pending.QuickEdit, error) {
	args, err := json.Marshal(idArgs{ID: j.ID})
	if err != nil {
		return pending.QuickEdit{}, err
	}
	c := tool.Call{TurnID: p.Button.ID, N: 1, Conv: p.Button.Conv, Transport: p.Transport, Principal: p.Button.Principal,
		Name: ActionRunNow, Args: args}
	id, err := s.Buttons.Ask(ctx, c, journal.Redact(runNowQuestion+s.summary(j, p.Button.Conv)))
	if err != nil {
		return pending.QuickEdit{}, err
	}
	q, err := s.Buttons.Outgoing(ctx, id)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: q.Text, Buttons: q.Buttons}, nil
}

// title is "task" or "reminder", marked when it is a system job.
func (s Scheduling) title(j schedule.Job) string {
	if j.System {
		return string(j.Kind) + systemMarker
	}
	return string(j.Kind)
}

// timing describes when a job runs, in the node's zone.
func (s Scheduling) timing(j schedule.Job) string {
	zone := s.Loc.String()
	if j.Cron == "" {
		return "once at " + j.At.In(s.Loc).Format(timeLayout) + " (" + zone + ")"
	}
	text := fmt.Sprintf("every %q (%s)", j.Cron, zone)
	if next := s.next(j); next != "" {
		text += ", next at " + next
	}
	return text
}

// next is the job's next run in the node's zone; "" when there is none.
func (s Scheduling) next(j schedule.Job) string {
	if j.Cron == "" {
		return j.At.In(s.Loc).Format(timeLayout)
	}
	spec, err := schedule.ParseCron(j.Cron)
	if err != nil {
		return ""
	}
	if t := spec.Next(s.Now(), s.Loc); !t.IsZero() {
		return t.In(s.Loc).Format(timeLayout)
	}
	return ""
}

// summary is what a job does, line by line: the text, the schedule, and
// for a task its grant and who receives the answer. here is the
// conversation it is shown in.
func (s Scheduling) summary(j schedule.Job, here string) string {
	if j.Kind != schedule.KindTask {
		return "Text: " + j.Text + "\nSchedule: " + s.timing(j) + "\nSent to: " + replyTarget(j.Conv, here)
	}
	return "Instruction: " + j.Text +
		"\nSchedule: " + s.timing(j) +
		"\nTools: " + orNone(j.Grant.Tools) +
		"\nHosts web.fetch may reach: " + orNone(j.Grant.Hosts) +
		"\nReplies go to: " + replyTarget(j.Grant.ReplyTo, here)
}

// entry is a job's message in a list.
func (s Scheduling) entry(j schedule.Job) string {
	title := s.title(j)
	return strings.ToUpper(title[:1]) + title[1:] + ": " + j.Text + "\nSchedule: " + s.timing(j)
}

// detailsText is all of a job.
func (s Scheduling) detailsText(j schedule.Job, here string) string {
	return s.title(j) + " " + j.ID + "\n" + s.summary(j, here) + "\nNext run: " + orDash(s.next(j)) +
		"\nCreated: " + j.Created.In(s.Loc).Format(timeLayout)
}

// replyTarget names a conversation for the person reading in here.
func replyTarget(conv, here string) string {
	if conv == here {
		return textThisChat
	}
	return conv
}

func orNone(items []string) string {
	if len(items) == 0 {
		return textNone
	}
	return strings.Join(items, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
