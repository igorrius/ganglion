package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/schedule"
)

// Jobs reads scheduled jobs (schedule.Store).
type Jobs interface {
	Get(ctx context.Context, id string) (schedule.Job, error)
}

// CronHandler is an edge's turn handler: there is no agent and no
// conversation on an edge, so the one event it handles is the time of a
// module's cron job, which it runs with no model (R-RUN-12). Anything else
// is dropped with a log line.
func CronHandler(jobs Jobs, run func(ctx context.Context, module, id string) error, log *slog.Logger) func(context.Context, event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
	return func(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		if e.Kind != event.KindSchedule || e.Source != schedule.Source {
			log.Info("edge: an event with no handler on an edge", "kind", e.Kind, "source", e.Source)
			return nil, queue.StateUpdate{}, nil
		}
		var s event.Schedule
		if json.Unmarshal(e.Payload, &s) != nil || s.Job == "" {
			log.Info("edge: a schedule event without a job")
			return nil, queue.StateUpdate{}, nil
		}
		j, err := jobs.Get(ctx, s.Job)
		switch {
		case errors.Is(err, schedule.ErrNotFound):
			return nil, queue.StateUpdate{}, nil
		case err != nil:
			return nil, queue.StateUpdate{}, fmt.Errorf("edge: job %s: %w", s.Job, err)
		case j.Deleted || j.Kind != schedule.KindModule:
			log.Info("edge: a scheduled job that is not a module's", "job", j.ID, "kind", j.Kind)
			return nil, queue.StateUpdate{}, nil
		}
		if err := run(ctx, j.Text, e.IdemKey); err != nil {
			log.Warn("edge: module cron run failed", "job", j.ID, "module", j.Text, "err", err)
		}
		return nil, queue.StateUpdate{}, nil
	}
}
