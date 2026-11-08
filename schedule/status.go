package schedule

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

const (
	// lagWarn is how late a run may be put before /status warns.
	lagWarn = time.Minute
	// tickStale is how long without a tick before /status warns: Run ticks every minute.
	tickStale = 2 * time.Minute
)

// health is what /status shows of the scheduler: the last tick, whether it
// failed and since when, and how late the last run was put.
type health struct {
	mu      sync.Mutex
	last    time.Time
	failing time.Time // the first of the failing ticks in a row; zero when the last succeeded
	lag     time.Duration
}

// ticked records a tick at now and its outcome.
func (h *health) ticked(now time.Time, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = now
	switch {
	case err == nil:
		h.failing = time.Time{}
	case h.failing.IsZero():
		h.failing = now
	}
}

// put records how late a run was put.
func (h *health) put(lag time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lag = max(lag, 0)
}

// Status implements status.Source (R-OBS-11): the jobs, the next run, and a
// tick that fails, stalls or put its last run late.
func (s *Scheduler) Status(ctx context.Context, sc status.Scope) ([]status.Check, error) {
	jobs, err := s.st.active(ctx)
	if err != nil {
		return nil, err
	}
	c := status.Check{Section: status.SectionProblems, State: status.OK, Line: texts.StatusJobsNone}
	if len(jobs) > 0 {
		c.Line, c.Vars = texts.StatusJobsIdle, texts.Vars{"n": strconv.Itoa(len(jobs))}
		if next := s.next(jobs, sc.Now); !next.IsZero() {
			c.Line, c.Vars["time"] = texts.StatusJobs, status.Clock(next, sc)
		}
	}
	return []status.Check{s.h.judge(c, sc.Now)}, nil
}

// judge puts the tick's trouble, if any, on c.
func (h *health) judge(c status.Check, now time.Time) status.Check {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.Vars == nil {
		c.Vars = texts.Vars{}
	}
	switch {
	case !h.failing.IsZero():
		c.State, c.Reason, c.Since = status.Warn, texts.StatusReasonSchedFailing, h.failing
	case !h.last.IsZero() && now.Sub(h.last) > tickStale:
		c.State, c.Reason, c.Since = status.Warn, texts.StatusReasonSchedStalled, h.last
		c.Vars["for"] = status.Duration(now.Sub(h.last))
	case h.lag > lagWarn:
		c.State, c.Reason = status.Warn, texts.StatusReasonSchedLag
		c.Vars["lag"] = status.Duration(h.lag)
	}
	return c
}

// next is the earliest time after now that any job runs; zero when none will.
func (s *Scheduler) next(jobs []activeJob, now time.Time) time.Time {
	var next time.Time
	for _, a := range jobs {
		t := s.nextOf(a, now)
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	return next
}

// nextOf is when job a runs next: a one-time job's time until it has run, a
// cron job's next slot after now.
func (s *Scheduler) nextOf(a activeJob, now time.Time) time.Time {
	if a.Cron == "" {
		if a.last.IsZero() {
			return later(a.At, now) // a time gone runs at the next tick
		}
		return time.Time{}
	}
	spec, err := ParseCron(a.Cron)
	if err != nil {
		return time.Time{}
	}
	return spec.Next(now, s.loc)
}

// later is the later of two times.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
