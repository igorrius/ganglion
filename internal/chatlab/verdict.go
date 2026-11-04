package chatlab

import (
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/internal/text"
)

// Result is what a scenario came to over its repeats.
type Result string

// Results.
const (
	ResultPassed       Result = "passed"
	ResultFlaky        Result = "flaky"
	ResultFailed       Result = "failed"
	ResultInconclusive Result = "inconclusive"
	ResultNotRun       Result = "notrun"
)

// maxClaim is how much of a claim fits a table cell.
const maxClaim = 40

// Judgment is the judge's answer on one claim in one repeat.
type Judgment struct {
	Claim  string
	OK     bool
	Reason string
}

// Repeat is one run of a scenario.
type Repeat struct {
	Failures     []string   // failed facts; any fails the scenario
	Judgments    []Judgment // in step order
	Inconclusive string     // why this repeat does not count
	NotRun       string     // why it did not run
	Transcript   string
	Tokens       int
	CostUSD      float64 // of its turns; 0 through the CLI
}

// Outcome is a scenario's row in the overview.
type Outcome struct {
	Scenario, Why  string
	Result         Result
	Facts, Meaning string // table cells
	Repeats        []Repeat
}

// Decide applies the rules of the design's section 5.5.
func Decide(name, why string, rs []Repeat) Outcome {
	o := Outcome{Scenario: name, Why: why, Repeats: rs}
	var counted []Repeat
	inconclusive, notRun, inconclusiveWhy, notRunWhy := 0, 0, "", ""
	for _, r := range rs {
		switch {
		case len(r.Failures) > 0:
			o.Result, o.Facts = ResultFailed, r.Failures[0]
		case r.Inconclusive != "":
			inconclusive++
			inconclusiveWhy = r.Inconclusive
		case r.NotRun != "":
			notRun++
			notRunWhy = r.NotRun
		default:
			counted = append(counted, r)
		}
	}
	if o.Result == ResultFailed {
		o.Meaning = sample(counted, inconclusive, notRun)
		return o
	}
	n := len(counted)
	if n == 0 {
		o.Result, o.Facts = ResultNotRun, fmt.Sprintf("%s; 0 of %d counted", notRunWhy, len(rs))
		if inconclusive > 0 {
			o.Result, o.Facts = ResultInconclusive, fmt.Sprintf("%s; 0 of %d counted", inconclusiveWhy, len(rs))
		}
		return o
	}
	extra := notes(inconclusive, notRun)
	o.Facts = fmt.Sprintf("held, %d of %d%s", n, n, extra)
	o.Result, o.Meaning = judge(counted)
	if o.Meaning != "" {
		o.Meaning += extra
	}
	// Passed means every repeat: a smaller sample must not read as one.
	if o.Result == ResultPassed {
		switch {
		case inconclusive > 0:
			o.Result = ResultInconclusive
		case notRun > 0:
			o.Result = ResultNotRun
		}
	}
	return o
}

// sample says how many repeats a failed scenario also ran.
func sample(counted []Repeat, inconclusive, notRun int) string {
	n := len(counted)
	if n == 0 && inconclusive == 0 && notRun == 0 {
		return ""
	}
	return fmt.Sprintf("%d others held%s", n, notes(inconclusive, notRun))
}

func notes(inconclusive, notRun int) string {
	var b strings.Builder
	if inconclusive > 0 {
		fmt.Fprintf(&b, ", %d inconclusive", inconclusive)
	}
	if notRun > 0 {
		fmt.Fprintf(&b, ", %d not run", notRun)
	}
	return b.String()
}

// judge takes the worst judge step, as k of n over the counted repeats.
func judge(counted []Repeat) (Result, string) {
	n := len(counted)
	steps := 0
	for _, r := range counted {
		steps = max(steps, len(r.Judgments))
	}
	result, meaning, worst := ResultPassed, "", n+1
	for step := range steps {
		k, claim := 0, ""
		for _, r := range counted {
			if step < len(r.Judgments) {
				claim = r.Judgments[step].Claim
				if r.Judgments[step].OK {
					k++
				}
			}
		}
		if k < worst {
			worst = k
			meaning = fmt.Sprintf("%s %d of %d", text.Truncate(claim, maxClaim), k, n)
		}
	}
	switch {
	case steps == 0 || worst == n:
	case worst == 0:
		result = ResultFailed
	default:
		result = ResultFlaky
	}
	return result, meaning
}

// ExitCode is chatlab run's exit code: 0 only if every scenario passed, 1 on
// a failure, 2 when the worst is flaky, inconclusive or not run.
func ExitCode(os []Outcome) int {
	code := 0
	if len(os) == 0 {
		code = 2
	}
	for _, o := range os {
		switch o.Result {
		case ResultFailed:
			return 1
		case ResultPassed:
		case ResultFlaky, ResultInconclusive, ResultNotRun:
			code = 2
		}
	}
	return code
}
