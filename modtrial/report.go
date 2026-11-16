package modtrial

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Report is the outcome of a trial run.
type Report struct {
	Examples []ExampleResult `json:"examples"`
	Now      *StepResult     `json:"now,omitempty"` // the run on the node's current cells; shown, never judged
	Passed   bool            `json:"passed"`
	Refused  string          `json:"refused,omitempty"` // the examples file's refusal, node words
}

// ExampleResult is one example's outcome.
type ExampleResult struct {
	Name   string       `json:"name"`
	Passed bool         `json:"passed"`
	Steps  []StepResult `json:"steps"`
}

// StepResult is one step's outcome.
type StepResult struct {
	N        int        `json:"n"` // 1-based
	Ran      bool       `json:"ran"`
	OK       bool       `json:"ok"`
	Kind     string     `json:"kind,omitempty"`     // failure kind: module.trap, module.timeout, module.format, a module's kind
	Messages []string   `json:"messages,omitempty"` // accepted notify texts: the owner's eyes only
	Reports  []Reported `json:"reports,omitempty"`
	Mismatch string     `json:"mismatch,omitempty"` // node words: "expected 1 message, got 0"

	out json.RawMessage // the module's answer, for this package's tests
}

// Reported is a cell value a module reported in a step.
type Reported struct {
	Cell  string          `json:"cell"`
	Value json.RawMessage `json:"value"`
}

// ModelLine is the report in the node's words, for the model: counts,
// example names, step numbers, mismatches and failure kinds; never a
// message text or a reported value.
func (r Report) ModelLine() string {
	if r.Refused != "" {
		return "trial: " + FileName + " refused: " + strings.TrimPrefix(r.Refused, FileName+": ")
	}
	passed := 0
	var problems []string
	for _, e := range r.Examples {
		if e.Passed {
			passed++
			continue
		}
		for _, s := range e.Steps {
			if s.OK {
				continue
			}
			problems = append(problems, fmt.Sprintf("%q step %d: %s", e.Name, s.N, s.problem()))
		}
	}
	head := fmt.Sprintf("trial: %d examples", len(r.Examples))
	if len(r.Examples) == 1 {
		head = "trial: 1 example"
	}
	if r.Passed {
		return head + ", all passed"
	}
	head += fmt.Sprintf(", %d passed", passed)
	return head + "; " + strings.Join(problems, "; ")
}

func (s StepResult) problem() string {
	switch {
	case !s.Ran:
		return "not run (the time budget ran out)"
	case s.Mismatch != "" && s.Kind != "":
		return s.Mismatch + "; failed (" + s.Kind + ")"
	case s.Mismatch != "":
		return s.Mismatch
	case s.Kind != "":
		return "failed (" + s.Kind + ")"
	}
	return "failed"
}
