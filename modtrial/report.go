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
	// Error says a trial could not run or did not finish ("the build does not
	// load on this node"), in node words. A report with Error is not passed.
	Error string `json:"error,omitempty"`
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
	Emitted  []Emitted  `json:"emitted,omitempty"`  // events the module emitted: the owner's eyes only
	Mismatch string     `json:"mismatch,omitempty"` // node words: "expected 1 message, got 0"
	Skipped  string     `json:"skipped,omitempty"`  // node words: why the step did not run

	out json.RawMessage // the module's answer, for this package's tests
}

// Reported is a cell value a module reported in a step.
type Reported struct {
	Cell  string          `json:"cell"`
	Value json.RawMessage `json:"value"`
}

// Emitted is an event a module emitted in a step, on one of its emits ports.
type Emitted struct {
	Port    string          `json:"port"`
	Payload json.RawMessage `json:"payload"`
}

// Why a step did not run (StepResult.Skipped), in node words.
const (
	SkipEarlier = "an earlier step failed"
	SkipBudget  = "the time budget ran out"
)

// ModelLineBytes bounds ModelLine.
const ModelLineBytes = 2000

// ModelLine is the report in the node's words, for the model: counts,
// example names, step numbers, mismatches and failure kinds; never a
// message text or a reported value. Per example it names the first failing
// step and sums up the steps after it that did not run. The line is at most
// ModelLineBytes long.
func (r Report) ModelLine() string {
	if r.Error != "" {
		return "trial: " + r.Error
	}
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
		problems = append(problems, e.problems()...)
	}
	head := fmt.Sprintf("trial: %d examples", len(r.Examples))
	if len(r.Examples) == 1 {
		head = "trial: 1 example"
	}
	if r.Passed {
		return head + ", all passed"
	}
	head += fmt.Sprintf(", %d passed", passed)
	return bounded(head, problems)
}

// bounded joins the problems after head, leaving out those that would pass
// ModelLineBytes and saying how many.
func bounded(head string, problems []string) string {
	out := head
	for i, p := range problems {
		more := fmt.Sprintf("; … and %d more", len(problems)-i)
		if len(out)+len("; ")+len(p)+len(more) > ModelLineBytes {
			return out + more
		}
		out += "; " + p
	}
	return out
}

// problems are the example's first failing step and, when steps after it
// did not run, one line for them. A step that did not run only because of
// an earlier failure is not a problem of its own.
func (e ExampleResult) problems() []string {
	var out []string
	var rest []int // the steps after the first problem that did not run
	var reason string
	for _, s := range e.Steps {
		if s.OK {
			continue
		}
		if len(out) == 0 {
			out = append(out, fmt.Sprintf("%q step %d: %s", e.Name, s.N, s.problem()))
			continue
		}
		if !s.Ran {
			rest, reason = append(rest, s.N), s.skipReason()
		}
	}
	if len(rest) > 0 {
		out = append(out, fmt.Sprintf("%q %s not run: %s", e.Name, stepRange(rest), reason))
	}
	if len(out) == 0 { // failed with no failing step: it did not load or ran out of time
		out = append(out, fmt.Sprintf("%q did not run", e.Name))
	}
	return out
}

// stepRange is "step 3" or "steps 3–5" for the step numbers n, in order.
func stepRange(n []int) string {
	if len(n) == 1 {
		return fmt.Sprintf("step %d", n[0])
	}
	return fmt.Sprintf("steps %d–%d", n[0], n[len(n)-1])
}

func (s StepResult) skipReason() string {
	if s.Skipped == SkipEarlier {
		return SkipEarlier
	}
	return SkipBudget
}

func (s StepResult) problem() string {
	switch {
	case !s.Ran:
		return "not run (" + s.skipReason() + ")"
	case s.Mismatch != "" && s.Kind != "":
		return s.Mismatch + "; failed (" + s.Kind + ")"
	case s.Mismatch != "":
		return s.Mismatch
	case s.Kind != "":
		return "failed (" + s.Kind + ")"
	}
	return "failed"
}
