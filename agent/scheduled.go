package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/turns"
)

// Jobs reads scheduled jobs (schedule.Store).
type Jobs interface {
	Get(ctx context.Context, id string) (schedule.Job, error)
}

// taskSpeaker is who says a task's instruction in its history.
const taskSpeaker = "Scheduled task"

// scheduled handles the time of a scheduled job (design section 5.3).
// Only the scheduler's events count; a schedule event from any other
// source does nothing. The job is read now, so one deleted after its event was put does nothing,
// and an event that does not fit its job is dropped. A reminder is sent
// as it is, with no model and no tools, and ends the turn. A task goes on
// as a turn of its own conversation: the owner is the principal (an
// admin check at run time decides the tools), the fixed instruction is
// the message, the grant is on every call, and the answer goes to the
// grant's reply target. A system job runs its registered code, without
// a model, and ends the turn.
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
		t.replyConv = j.Conv
		t.reply(j.Text)
		return true, nil
	case schedule.KindTask:
		g := j.Grant
		t.grant, t.replyConv = &g, g.ReplyTo
		t.record.Outcome = turns.OutcomeOK
		return false, nil
	case schedule.KindSystem:
		t.replyConv = j.Conv
		return true, a.system(ctx, t, j)
	default:
		a.log.Warn("agent: scheduled job of an unknown kind", "job", j.ID, "kind", j.Kind)
		return true, nil
	}
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
	report, err := run(ctx)
	if err != nil {
		return fmt.Errorf("agent: system job %s: %w", j.ID, err)
	}
	t.reply(report)
	return nil
}
