// Package schedule runs jobs at times: reminders and scheduled agent
// tasks (spec 7.10, R-SCH-1, R-SCH-2, R-POL-8; design section 5.3).
// Cron expressions are read in the node's time zone by an own
// five-field parser. Every scheduled run is recorded in cron_runs, and
// after downtime each missed job runs once. A job's time becomes an
// event in the inbox, which the agent handles: a reminder is sent as it
// is, a task runs as a turn of its own conversation "task:<id>".
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/tool"
)

// Source is the Source of the events the scheduler puts.
const Source = "cron"

// Kind is what a job does. Kinds are stored, so their values never
// change.
type Kind string

// Kinds.
const (
	// KindReminder sends a fixed text at a time, without a model.
	KindReminder Kind = "reminder"
	// KindTask runs an instruction as a full agent turn with the tools of
	// its grant.
	KindTask Kind = "task"
	// KindSystem runs code the node registered under the job's id, such
	// as the memory audit, without a model, and sends its report to the
	// job's conversation (ruling R36). Only the node makes one (System).
	KindSystem Kind = "system"
)

// Errors; callers decide on them with errors.Is.
var (
	// ErrNotFound is a job that does not exist, is deleted where that
	// matters, or belongs to someone else.
	ErrNotFound = errors.New("schedule: no such job")
	// ErrExists is a job id that is taken.
	ErrExists = errors.New("schedule: job exists")
	// ErrSystem is an attempt to delete a system job.
	ErrSystem = errors.New("schedule: a system job cannot be deleted")
	// ErrNotTask is a run now of a job that is not a task.
	ErrNotTask = errors.New("schedule: only a task can run now")
	// ErrInvalid is a job that cannot be stored as it is.
	ErrInvalid = errors.New("schedule: invalid job")
)

// Job is a reminder or a task. Its text and grant are fixed at creation:
// nothing a run reads can change them (R-POL-8).
type Job struct {
	ID        string
	Owner     string // the principal; a task runs as them, with their rights at run time
	Conv      string // where the job was made; a reminder is sent here
	Transport string // the transport replies go out on
	Kind      Kind
	Cron      string    // five fields in the node's zone; "" for one time
	At        time.Time // the one time; zero for cron
	Text      string    // the reminder's text or the task's instruction
	Grant     tool.Grant
	System    bool // made by the node (the memory audit); cannot be deleted
	Created   time.Time
	Deleted   bool
	DeletedBy string // the token of the press or call that deleted it; "" while live
}

// EventConv is the conversation of the job's events: a reminder's or a
// system job's own conversation, a task's "task:<id>", whose history is
// its earlier runs.
func (j Job) EventConv() string {
	if j.Kind == KindTask {
		return tool.TaskConvPrefix + j.ID
	}
	return j.Conv
}

// validate checks a job before it is stored.
func (j Job) validate() error {
	switch {
	case j.ID == "" || j.Owner == "" || j.Conv == "":
		return fmt.Errorf("%w %q: it needs an id, an owner and a conversation", ErrInvalid, j.ID)
	case isBlank(j.Text):
		return fmt.Errorf("%w %s: no text", ErrInvalid, j.ID)
	case j.Created.IsZero():
		return fmt.Errorf("%w %s: no creation time", ErrInvalid, j.ID)
	case (j.Cron == "") == j.At.IsZero():
		return fmt.Errorf("%w %s: it needs a cron or a time, not both", ErrInvalid, j.ID)
	}
	if j.Cron != "" {
		if _, err := ParseCron(j.Cron); err != nil {
			return fmt.Errorf("%w %s: %w", ErrInvalid, j.ID, err)
		}
	}
	return j.validateKind()
}

// validateKind checks what depends on the kind: a reminder and a system
// job have no grant, a task has someone to reply to, and only the node
// makes a system job.
func (j Job) validateKind() error {
	switch j.Kind {
	case KindReminder, KindSystem:
		if len(j.Grant.Tools) > 0 || len(j.Grant.Hosts) > 0 || j.Grant.ReplyTo != "" {
			return fmt.Errorf("%w %s: a %s job has no grant", ErrInvalid, j.ID, j.Kind)
		}
		if j.Kind == KindSystem && !j.System {
			return fmt.Errorf("%w %s: only the node makes a system job", ErrInvalid, j.ID)
		}
	case KindTask:
		if j.Grant.ReplyTo == "" {
			return fmt.Errorf("%w %s: a task needs a conversation to reply to", ErrInvalid, j.ID)
		}
	default:
		return fmt.Errorf("%w %s: unknown kind %q", ErrInvalid, j.ID, j.Kind)
	}
	return nil
}

// Inbox stores events (queue.Queue); an IdemKey already stored is a
// no-op.
type Inbox interface {
	Put(ctx context.Context, e event.Event) error
}

// AfterFunc waits like time.After.
type AfterFunc func(d time.Duration) <-chan time.Time

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithAfter sets how the scheduler waits between ticks; the default is
// time.After.
func WithAfter(f AfterFunc) Option { return func(s *Scheduler) { s.after = f } }

// WithLogger sets where a failed tick is reported. The default drops it.
func WithLogger(l *slog.Logger) Option { return func(s *Scheduler) { s.log = l } }

// Scheduler is a node component: at every minute of the clock it puts
// an event for each job whose time has come.
type Scheduler struct {
	st    *Store
	q     Inbox
	loc   *time.Location
	now   func() time.Time
	after AfterFunc
	log   *slog.Logger
	in    instruments
	h     health // what /status shows
}

// New returns a scheduler of st's jobs in loc (R-SCH-1) that puts their
// events into q.
func New(st *Store, q Inbox, loc *time.Location, now func() time.Time, opts ...Option) *Scheduler {
	s := &Scheduler{st: st, q: q, loc: loc, now: now, after: time.After, log: slog.New(slog.DiscardHandler), in: newInstruments()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements node.Component.
func (*Scheduler) Name() string { return "scheduler" }

// Run ticks at once and then at each minute boundary until ctx ends; it
// returns nil then. A failed tick is logged and the next one tries again.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("schedule: tick failed", "err", err)
		}
		now := s.now()
		select {
		case <-ctx.Done():
			return nil
		case <-s.after(now.Truncate(time.Minute).Add(time.Minute).Sub(now)):
		}
	}
}

// Tick puts the events that are due: first those of runs recorded but
// not put (a crash in between), then for each job at most one, for the
// latest of its slots that came since its last run. That is the catch-up
// rule: after downtime each missed job runs once (R-SCH-1). The error
// joins the failures of the jobs that could not fire.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.now()
	err := s.tick(ctx, now)
	s.h.ticked(now, err)
	return err
}

// tick is Tick at now.
func (s *Scheduler) tick(ctx context.Context, now time.Time) error {
	if err := s.putUnput(ctx, now); err != nil {
		return err
	}
	jobs, err := s.st.active(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range jobs {
		slot, ok := s.due(a, now)
		if !ok {
			continue
		}
		// One job that fails does not hold back the others; the next
		// tick tries it again.
		if err := s.fire(ctx, a.Job, slot, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// due is the slot a job runs for now: the latest one at or before now
// and after its last run (or its creation).
func (s *Scheduler) due(a activeJob, now time.Time) (time.Time, bool) {
	if a.Cron == "" {
		return a.At, a.last.IsZero() && !a.At.After(now)
	}
	spec, err := ParseCron(a.Cron)
	if err != nil { // stored jobs were validated; a bad one never runs
		s.log.Error("schedule: stored job has a bad cron", "job", a.ID, "err", err)
		return time.Time{}, false
	}
	base := a.Created
	if a.last.After(base) {
		base = a.last
	}
	var slot time.Time
	for t := spec.Next(base, s.loc); !t.IsZero() && !t.After(now); t = spec.Next(t, s.loc) {
		slot = t
	}
	return slot, !slot.IsZero()
}

// fire records the run, unless the job was deleted meanwhile, and then
// puts its event. Both are idempotent, so a crash between them is made
// good by the next tick (putUnput).
func (s *Scheduler) fire(ctx context.Context, j Job, slot, now time.Time) error {
	claimed, err := s.st.claim(ctx, j.ID, slot)
	if err != nil || !claimed {
		return err
	}
	return s.put(ctx, j, slotKey(j.ID, slot), slot, now)
}

// putUnput puts the events of runs recorded but not put.
func (s *Scheduler) putUnput(ctx context.Context, now time.Time) error {
	runs, err := s.st.unput(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if err := s.put(ctx, r.job, slotKey(r.job.ID, r.slot), r.slot, now); err != nil {
			return err
		}
	}
	return nil
}

// put stores the job's event and, for a scheduled run, marks it put.
func (s *Scheduler) put(ctx context.Context, j Job, key string, slot, now time.Time) error {
	err := s.q.Put(ctx, jobEvent(j, key, slot, now))
	s.in.observeRun(ctx, j.Kind, err)
	if err != nil {
		return fmt.Errorf("schedule: put the event of %s: %w", j.ID, err)
	}
	s.in.observeLag(ctx, j.Kind, now.Sub(slot))
	s.h.put(now.Sub(slot))
	return s.st.markPut(ctx, j.ID, slot, now)
}

// RunNow puts an event that runs task id of owner now, outside its
// schedule, keyed by the current time. ErrNotFound for a job that is not
// owner's or is deleted, ErrNotTask for a reminder. A caller that may be
// rerun uses RunKey.
func (s *Scheduler) RunNow(ctx context.Context, id, owner string) error {
	return s.RunKey(ctx, id, owner, strconv.FormatInt(s.now().UnixMilli(), 10))
}

// RunKey is RunNow for the run named by token, such as the key of the
// confirmed call or the id of the press that asked for it: the same token
// is the same run, so a rerun caller starts it once. The event's key is
// "cron:<id>:now:<token>".
func (s *Scheduler) RunKey(ctx context.Context, id, owner, token string) error {
	j, err := s.st.Get(ctx, id)
	switch {
	case err != nil:
		return err
	case j.Deleted || j.Owner != owner:
		return fmt.Errorf("schedule: run %s: %w", id, ErrNotFound)
	case j.Kind != KindTask:
		return fmt.Errorf("schedule: run %s: %w", id, ErrNotTask)
	}
	if token == "" {
		return fmt.Errorf("schedule: run %s: %w: no token", id, ErrInvalid)
	}
	now := s.now()
	err = s.q.Put(ctx, jobEvent(j, "cron:"+id+":now:"+token, now, now))
	s.in.observeRun(ctx, j.Kind, err)
	if err != nil {
		return fmt.Errorf("schedule: put the event of %s: %w", id, err)
	}
	return nil
}

// slotKey is the idempotency key of a scheduled run: the job and its
// slot (R-SCH-1).
func slotKey(id string, slot time.Time) string {
	return "cron:" + id + ":" + strconv.FormatInt(slot.UnixMilli(), 10)
}

// jobEvent is the event of a run of j.
func jobEvent(j Job, key string, slot, now time.Time) event.Event {
	payload, err := json.Marshal(event.Schedule{Job: j.ID, Scheduled: slot.UnixMilli()})
	if err != nil {
		panic("schedule: a struct of a string and a number always marshals: " + err.Error())
	}
	return event.Event{
		ID: event.NewID(now), IdemKey: key, Source: Source, Conv: j.EventConv(),
		Kind: event.KindSchedule, Payload: payload, At: now,
	}
}
