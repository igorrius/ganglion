package backup

import (
	"context"
	"errors"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// StaleAfter is how long a node may go without a successful backup before
// /status warns (R-BAK-5): a day's run plus a day's slack and some hours.
const StaleAfter = 36 * time.Hour

// Status is the status source of the backups (R-BAK-5). It reads the last
// result from the data directory, so it answers the same after a restart.
type Status struct {
	on    bool
	dir   string
	start time.Time
}

// NewStatus makes the source for a node whose data directory is dir and that
// started at start. With on false it says backups are off.
func NewStatus(on bool, dir string, start time.Time) *Status {
	return &Status{on: on, dir: dir, start: start}
}

// Name implements status.Source.
func (s *Status) Name() string { return "backup" }

// Status implements status.Source: off without the section; the last
// copy's time and size; a warning after StaleAfter without one, counted from
// the start for a node that has made none; and a failing line with its reason
// while the last run failed.
func (s *Status) Status(_ context.Context, sc status.Scope) ([]status.Check, error) {
	if !s.on {
		return []status.Check{{Section: status.SectionProblems, State: status.Off, Line: texts.StatusBackupOff}}, nil
	}
	res, err := Last(s.dir)
	switch {
	case errors.Is(err, ErrNoResult):
		return []status.Check{s.none(sc)}, nil
	case err != nil:
		return nil, err
	case !res.OK:
		return []status.Check{{Section: status.SectionProblems, State: status.Fail, Line: texts.StatusBackupFailed,
			Reason: ReasonKey(res.Kind), Vars: texts.Vars{"detail": res.Detail}, Since: res.Time}}, nil
	}
	return []status.Check{s.copyLine(res, sc)}, nil
}

// none is the line of a node that has made no copy: fine until StaleAfter
// after its start, a warning then.
func (s *Status) none(sc status.Scope) status.Check {
	waited := sc.Now.Sub(s.start)
	if waited <= StaleAfter {
		return status.Check{Section: status.SectionProblems, State: status.OK, Line: texts.StatusBackupWaiting}
	}
	return status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusBackupNone,
		Reason: texts.StatusReasonBackupStale, Spans: map[string]time.Duration{"for": waited}}
}

// copyLine is the line of a last run that succeeded.
func (s *Status) copyLine(res Result, sc status.Scope) status.Check {
	c := status.Check{Section: status.SectionProblems, State: status.OK, Line: texts.StatusBackupOK,
		Vars: texts.Vars{"time": status.Clock(res.LastOK, sc), "size": status.Bytes(res.Size)}}
	if age := sc.Now.Sub(res.LastOK); age > StaleAfter {
		c.State, c.Line, c.Reason = status.Warn, texts.StatusBackupStale, texts.StatusReasonBackupStale
		c.Spans = map[string]time.Duration{"for": age}
	}
	return c
}
