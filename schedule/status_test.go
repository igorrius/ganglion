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
	cs := e.sched.Status(context.Background(), status.Scope{Now: e.now, Location: e.loc})
	if len(cs) != 1 || cs[0].Section != status.SectionProblems {
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
	e.now = time.Date(2026, 11, 3, 9, 1, 30, 0, time.UTC) // the 09:00 slot put 90 s late
	e.tick(t)
	c := e.jobsLine(t)
	if c.State != status.Warn || c.Reason != texts.StatusReasonSchedLag || c.Vars["lag"] != "1 min" {
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
	if c.State != status.Warn || c.Reason != texts.StatusReasonSchedStalled || c.Vars["for"] != "3 min" {
		t.Errorf("stalled: %+v", c)
	}
}
