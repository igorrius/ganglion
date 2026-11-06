package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// Placeholders that several texts share.
const (
	varText    = "text"
	varSummary = "summary"
	varTarget  = "target"
	varZone    = "zone"
)

// What a press notes for the model, in English.
const (
	noteDeleted   = "the owner deleted %s %s: %s"
	noteRan       = "the owner ran %s %s now: %s"
	noteHeadRunes = 100
)

// Symbols on the buttons that act, outside the translated label (R-LANG-6). Details only shows.
const (
	markDelete = "🗑 "
	markRunNow = "▶️ "
)

// buttonLabel is the label of a quick action in sp's language.
func buttonLabel(sp texts.Speaker, action string) string {
	switch action {
	case ActionDelete:
		return markDelete + sp.Label(texts.ButtonDelete)
	case ActionRunNow:
		return markRunNow + sp.Label(texts.ButtonRunNow)
	default:
		return sp.Label(texts.ButtonDetails)
	}
}

// QuickActions are the buttons of a listed job by action name, for the
// agent (agent.Deps.QuickActions). Each edits the job's own message.
func (s Scheduling) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionDetails: s.details, ActionDelete: s.deleteByButton, ActionRunNow: s.runByButton}
}

// buttons stores the quick-action buttons of job j for principal in conv
// and returns them, in the order of actions: Delete only for a job that
// is not a system job, Run now only for a task.
func (s Scheduling) buttons(ctx context.Context, sp texts.Speaker, j schedule.Job, principal, conv string, actions ...string) ([]event.Button, error) {
	args, err := json.Marshal(idArgs{ID: j.ID})
	if err != nil {
		return nil, err
	}
	var stored []pending.Button
	for _, action := range actions {
		if action == ActionDelete && j.System || action == ActionRunNow && j.Kind != schedule.KindTask {
			continue
		}
		stored = append(stored, pending.Button{ID: pending.NewButtonID(), Action: action, Label: buttonLabel(sp, action),
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
		return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, err
	}
	buttons, err := s.buttons(ctx, p.Speak, j, p.Button.Principal, p.Button.Conv, ActionDelete, ActionRunNow)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: s.detailsText(p.Speak, j, p.Button.Conv), Buttons: buttons}, nil
}

// deleteByButton deletes the job at once: the press is the owner's own
// decision, so there is no second question.
func (s Scheduling) deleteByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	j, ok, err := s.pressedJob(ctx, p)
	if err == nil && !ok {
		return s.deletedBy(ctx, p)
	}
	if err != nil {
		return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, err
	}
	err = s.Jobs.Delete(ctx, j.ID, p.Button.Principal, p.Event)
	switch {
	case errors.Is(err, schedule.ErrSystem):
		return pending.QuickEdit{Text: s.entry(p.Speak, j) + "\n\n" + p.Speak.Text(texts.SchedNoDelete, nil)}, nil
	case errors.Is(err, schedule.ErrNotFound):
		return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, nil
	case err != nil:
		return pending.QuickEdit{}, err
	}
	return s.deletedEdit(p, j), nil
}

// deletedEdit is the job's message once deleted, with the note for the
// history (ruling R42).
func (s Scheduling) deletedEdit(p pending.QuickPress, j schedule.Job) pending.QuickEdit {
	return pending.QuickEdit{Text: s.entry(p.Speak, j) + "\n\n" + p.Speak.Text(texts.SchedDeleted, nil), Note: fmt.Sprintf(noteDeleted, j.Kind, j.ID, head(j.Text))}
}

// deletedBy answers a Delete press of a job that is not there: the same
// edit and note again when this press deleted it (a rerun of its event
// after a crash), "no longer exists" otherwise.
func (s Scheduling) deletedBy(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var a idArgs
	if err := json.Unmarshal(p.Button.Args, &a); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("schedule: button %s: arguments: %w", p.Button.ID, err)
	}
	j, err := s.Jobs.Get(ctx, a.ID)
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, nil
	case err != nil:
		return pending.QuickEdit{}, err
	case j.Deleted && j.DeletedBy == p.Event && j.Owner == p.Button.Principal:
		return s.deletedEdit(p, j), nil
	}
	return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, nil
}

// head is the start of a job's text on one line, for a note.
func head(s string) string {
	return text.Truncate(strings.Join(strings.Fields(s), " "), noteHeadRunes)
}

// runByButton runs a task now. A task whose grant may act on the world
// (a side-effect tool, or one no longer registered) asks first, in place
// of the job's message; one that only reads runs at once, keyed by the
// press's event, so handling the press again starts the same run.
func (s Scheduling) runByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	j, ok, err := s.pressedJob(ctx, p)
	switch {
	case err != nil || !ok:
		return pending.QuickEdit{Text: p.Speak.Text(texts.SchedGone, nil)}, err
	case j.Kind != schedule.KindTask:
		return pending.QuickEdit{Text: s.entry(p.Speak, j) + "\n\n" + p.Speak.Text(texts.SchedNotTask, nil)}, nil
	case s.mayAct(j):
		return s.askRunNow(ctx, p, j)
	}
	if err := s.Runner.RunKey(ctx, j.ID, p.Button.Principal, p.Event); err != nil {
		return pending.QuickEdit{}, err
	}
	buttons, err := s.buttons(ctx, p.Speak, j, p.Button.Principal, p.Button.Conv, ActionDetails, ActionDelete, ActionRunNow)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	shown := s.entry(p.Speak, j) + "\n\n" + p.Speak.Text(texts.SchedStarted, texts.Vars{varTarget: replyTarget(p.Speak, j.Grant.ReplyTo, p.Button.Conv)})
	return pending.QuickEdit{Text: shown, Buttons: buttons, Note: fmt.Sprintf(noteRan, j.Kind, j.ID, head(j.Text))}, nil
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
	c := tool.Call{Speak: p.Speak, TurnID: p.Button.ID, N: 1, Conv: p.Button.Conv, Transport: p.Transport, Principal: p.Button.Principal,
		Name: ActionRunNow, Args: args}
	id, err := s.Buttons.Ask(ctx, c, redact.String(p.Speak.Text(texts.AskRunNow, texts.Vars{varSummary: s.summary(p.Speak, j, p.Button.Conv)})))
	if err != nil {
		return pending.QuickEdit{}, err
	}
	q, err := s.Buttons.Outgoing(ctx, id)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: q.Text, Buttons: q.Buttons, Key: q.IdemKey}, nil
}

// title names a job's kind in sp's language.
func (s Scheduling) title(sp texts.Speaker, j schedule.Job) string {
	kind := sp.Text(texts.SchedKindTask, nil)
	if j.Kind == schedule.KindReminder {
		kind = sp.Text(texts.SchedKindReminder, nil)
	}
	if j.System {
		return sp.Text(texts.SchedSystem, texts.Vars{"kind": kind})
	}
	return kind
}

// timing describes when a job runs, in the node's zone.
func (s Scheduling) timing(sp texts.Speaker, j schedule.Job) string {
	zone := s.Loc.String()
	if j.Cron == "" {
		return sp.Text(texts.SchedOnce, texts.Vars{"at": j.At.In(s.Loc).Format(timeLayout), varZone: zone})
	}
	if next := s.next(j); next != "" {
		return sp.Text(texts.SchedEveryNext, texts.Vars{"cron": j.Cron, varZone: zone, "next": next})
	}
	return sp.Text(texts.SchedEvery, texts.Vars{"cron": j.Cron, varZone: zone})
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
func (s Scheduling) summary(sp texts.Speaker, j schedule.Job, here string) string {
	if j.Kind != schedule.KindTask {
		return sp.Text(texts.SchedSummaryReminder, texts.Vars{varText: j.Text, "timing": s.timing(sp, j), varTarget: replyTarget(sp, j.Conv, here)})
	}
	return sp.Text(texts.SchedSummaryTask, texts.Vars{varText: j.Text, "timing": s.timing(sp, j),
		"tools": orNone(sp, j.Grant.Tools), "hosts": orNone(sp, j.Grant.Hosts), varTarget: replyTarget(sp, j.Grant.ReplyTo, here)})
}

// entry is a job's message in a list.
func (s Scheduling) entry(sp texts.Speaker, j schedule.Job) string {
	title := []rune(s.title(sp, j))
	title[0] = unicode.ToUpper(title[0])
	return sp.Text(texts.SchedEntry, texts.Vars{"title": string(title), varText: j.Text, "timing": s.timing(sp, j)})
}

// detailsText is all of a job.
func (s Scheduling) detailsText(sp texts.Speaker, j schedule.Job, here string) string {
	return sp.Text(texts.SchedDetails, texts.Vars{"title": s.title(sp, j), "id": j.ID, varSummary: s.summary(sp, j, here),
		"next": orDash(s.next(j)), "created": j.Created.In(s.Loc).Format(timeLayout)})
}

// replyTarget names a conversation for the person reading in here.
func replyTarget(sp texts.Speaker, conv, here string) string {
	if conv == here {
		return sp.Text(texts.SchedThisChat, nil)
	}
	return conv
}

func orNone(sp texts.Speaker, items []string) string {
	if len(items) == 0 {
		return sp.Text(texts.SchedNone, nil)
	}
	return strings.Join(items, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
