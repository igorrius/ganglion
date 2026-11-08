package modstore

import (
	"context"
	"slices"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// fallback is a stored version that failed its check at start, so the
// embedded build runs.
type fallback struct {
	version string
	at      time.Time
}

// noteFallback records that module's stored version failed at start.
func (s *Store) noteFallback(module, version string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.fellBack[module] = fallback{version: version, at: s.now()}
}

// Name implements status.Source.
func (*Store) Name() string { return "modules" }

// Status implements status.Source (R-OBS-11): one line for each module whose
// stored version failed its check at start; none when every module runs as
// chosen.
func (s *Store) Status(context.Context, status.Scope) []status.Check {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	names := make([]string, 0, len(s.fellBack))
	for m := range s.fellBack {
		names = append(names, m)
	}
	slices.Sort(names)
	out := make([]status.Check, 0, len(names))
	for _, m := range names {
		f := s.fellBack[m]
		out = append(out, status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusModuleFallback,
			Reason: texts.StatusReasonModuleFailed, Vars: texts.Vars{"module": m, "version": f.version}, Since: f.at})
	}
	return out
}
