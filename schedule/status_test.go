package schedule_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// jobsLine is the scheduler's one line at e's clock.
func (e *env) jobsLine(t *testing.T) status.Check {
	t.Helper()
	cs, err := e.sched.Status(context.Background(), status.Scope{Now: e.now, Location: e.loc})
	if err != nil || len(cs) != 1 || cs[0].Section != status.SectionProblems {
		t.Fatalf("lines %+v", cs)
	}
	return cs[0]
}

func TestStatusNextRun(t *testing.T) {
	e := newEnv(t)
	if c := e.jobsLine(t); c.Line != texts.StatusJobsNone || c.State != status.OK {
		t.Errorf("none: %+v", c)
	}
	e.create(t, task("hourly", "0 * * * *"))                                     // next 11:00 Kyiv (09:00 UTC)
	e.create(t, reminder("soon", time.Date(2026, 11, 3, 8, 30, 0, 0, time.UTC))) // 10:30 Kyiv
	e.tick(t)
	c := e.jobsLine(t)
	if c.Line != texts.StatusJobs || c.State != status.OK || c.Vars["n"] != "2" || c.Vars["time"] != "10:30" {
		t.Errorf("two jobs: %+v", c)
	}
}

func TestStatusLateRun(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("hourly", "0 * * * *"))
	e.tick(t)                                             // the node runs before the slot
	e.now = time.Date(2026, 11, 3, 9, 1, 30, 0, time.UTC) // the 09:00 slot put 90 s late
	e.tick(t)
	c := e.jobsLine(t)
	if c.State != status.Warn || c.Reason != texts.StatusReasonSchedLag || c.Spans["lag"] != 90*time.Second {
		t.Errorf("late: %+v", c)
	}
}

func TestStatusFailingTick(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("hourly", "0 * * * *"))
	e.now = t0.Add(time.Hour)
	e.q.fail = errors.New("disk full")
	if err := e.sched.Tick(context.Background()); err == nil {
		t.Fatal("no failure")
	}
	c := e.jobsLine(t)
	if c.State != status.Warn || c.Reason != texts.StatusReasonSchedFailing || !c.Since.Equal(e.now) {
		t.Errorf("failing: %+v", c)
	}
	e.tick(t)
	if c := e.jobsLine(t); c.Reason == texts.StatusReasonSchedFailing {
		t.Errorf("a good tick did not clear it: %+v", c)
	}
}

func TestStatusStalledTicks(t *testing.T) {
	e := newEnv(t)
	e.tick(t)
	e.now = e.now.Add(3 * time.Minute)
	c := e.jobsLine(t)
	if c.State != status.Warn || c.Reason != texts.StatusReasonSchedStalled || c.Spans["for"] != 3*time.Minute {
		t.Errorf("stalled: %+v", c)
	}
}

// Review: a one-time job past its time, not yet put, shows the next tick, not a time gone.
func TestStatusOverdueReminderIsNotInThePast(t *testing.T) {
	e := newEnv(t)
	e.create(t, reminder("late", t0.Add(-time.Hour)))
	if c := e.jobsLine(t); c.Line != texts.StatusJobs || c.Vars["time"] != "10:00" {
		t.Errorf("%+v", c)
	}
}

// Owner's look, 2026-11-08: a one-time job that has run is not counted, as the list does not show it.
func TestStatusCountsWhatTheListShows(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("hourly", "0 * * * *"))
	e.create(t, reminder("done", t0.Add(time.Minute)))
	e.now = t0.Add(2 * time.Minute)
	e.tick(t)
	if c := e.jobsLine(t); c.Vars["n"] != "1" {
		t.Errorf("%+v", c)
	}
}

// Owner's look, 2026-11-08: a run caught up after the node was down is the catch-up rule
// (R-SCH-1), not a late scheduler.
func TestStatusCatchUpIsNotLate(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("audit", "0 4 * * *")) // 04:00 Kyiv, as the memory audit
	e.now = t0.Add(20 * time.Hour)          // the node starts at 06:00 Kyiv the next day
	if got := e.tick(t); len(got) != 1 {
		t.Fatalf("no catch-up run: %+v", got)
	}
	if c := e.jobsLine(t); c.State != status.OK {
		t.Errorf("catch-up: %+v", c)
	}
}
