package skills

import (
	"context"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// kindReasons are the problem kinds' texts in /status.
var kindReasons = map[ProblemKind]texts.Key{
	ProblemShape: texts.StatusReasonSkillShape, ProblemRead: texts.StatusReasonSkillRead,
	ProblemFrontmatter: texts.StatusReasonSkillFrontmatter, ProblemScript: texts.StatusReasonSkillScript,
	ProblemBuiltinName: texts.StatusReasonSkillBuiltin,
}

// Refused is the skills loaded at start and those that were not, for /status
// (R-OBS-11).
type Refused struct {
	Problems []Problem
	Loaded   int
	Since    time.Time // when they were loaded
}

// Name implements status.Source.
func (Refused) Name() string { return "skills" }

// Status implements status.Source: how many skills are loaded, then each that
// is not with the kind of rule it broke, never the loader's text.
func (r Refused) Status(context.Context, status.Scope) []status.Check {
	out := []status.Check{{Section: status.SectionProblems, State: status.OK, Line: texts.StatusSkills,
		Vars: texts.Vars{"n": strconv.Itoa(r.Loaded)}}}
	for _, p := range r.Problems {
		out = append(out, status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusSkillRefused,
			Reason: kindReasons[p.Kind], Vars: texts.Vars{"skill": p.Skill}, Since: r.Since})
	}
	return out
}
