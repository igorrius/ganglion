package chatlab

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/redact"
)

// Run is one `chatlab run`: what the overview and the report show.
type Run struct {
	Started  time.Time
	Outcomes []Outcome
	Tokens   int
	Secrets  []string // provider keys, which no pattern can recognise
	// Untouched are scenarios left out, as nothing of theirs changed.
	Untouched []string
	// FullRun is why a full run is recommended; it is never started by itself.
	FullRun []string
}

var marks = map[Result]string{
	ResultPassed:       "✅ passed",
	ResultFlaky:        "🟡 flaky",
	ResultFailed:       "❌ failed",
	ResultInconclusive: "⚪ inconclusive",
	ResultNotRun:       "⏭ not run",
}

// clean removes what must not leave the run.
func (r Run) clean(s string) string { return redact.Values(r.Secrets...)(redact.String(s)) }

// cell keeps text on one row of a table.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	return strings.Join(strings.Fields(s), " ")
}

// Table is the overview for the chat: a row per scenario and a summary line.
func (r Run) Table() string {
	var b strings.Builder
	b.WriteString("| Scenario | Result | Facts | Meaning | Details |\n|---|---|---|---|---|\n")
	counts := map[Result]int{}
	cost := 0.0
	for _, o := range r.Outcomes {
		counts[o.Result]++
		for _, rep := range o.Repeats {
			cost += rep.CostUSD
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | transcript |\n", cell(o.Scenario), marks[o.Result], cell(o.Facts), cell(o.Meaning))
	}
	fmt.Fprintf(&b, "\n%d passed, %d flaky, %d failed, %d inconclusive, %d not run. Spent %d tokens, $%.4f.\n",
		counts[ResultPassed], counts[ResultFlaky], counts[ResultFailed], counts[ResultInconclusive], counts[ResultNotRun], r.Tokens, cost)
	fmt.Fprintf(&b, "%d ran, %d untouched.\n", len(r.Outcomes), len(r.Untouched))
	if len(r.FullRun) > 0 {
		fmt.Fprintf(&b, "Full run recommended, about 1,100,000 tokens, make lab-all, or make lab-skip-full to do without: %s.\n", strings.Join(r.FullRun, ", "))
	}
	return r.clean(b.String())
}

// report is the file: the table, then every repeat's details.
func (r Run) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Chatlab run, %s UTC\n\n%s", r.Started.UTC().Format(time.DateTime), r.Table())
	for _, o := range r.Outcomes {
		fmt.Fprintf(&b, "\n## %s: %s\n\nGuards against: %s\n", o.Scenario, marks[o.Result], o.Why)
		for i, rep := range o.Repeats {
			fmt.Fprintf(&b, "\n### Repeat %d of %d\n\nSpent %d tokens.\n", i+1, len(o.Repeats), rep.Tokens)
			if rep.Inconclusive != "" {
				fmt.Fprintf(&b, "\nInconclusive: %s\n", rep.Inconclusive)
			}
			if rep.NotRun != "" {
				fmt.Fprintf(&b, "\nNot run: %s\n", rep.NotRun)
			}
			for _, f := range rep.Failures {
				fmt.Fprintf(&b, "\n- ❌ %s", f)
			}
			for _, j := range rep.Judgments {
				mark := "✅"
				if !j.OK {
					mark = "❌"
				}
				fmt.Fprintf(&b, "\n- %s judge: %s (%s)", mark, j.Claim, j.Reason)
			}
			fmt.Fprintf(&b, "\n\n%s\n", rep.Transcript)
		}
	}
	return r.clean(b.String())
}

// WriteReport writes <dir>/<UTC time>/report.md, readable only by this user,
// and returns its path.
func WriteReport(dir string, r Run) (string, error) {
	at := filepath.Join(dir, r.Started.UTC().Format("20060102-150405"))
	if err := os.MkdirAll(at, 0o700); err != nil {
		return "", fmt.Errorf("chatlab: report: %w", err)
	}
	path := filepath.Join(at, "report.md")
	if err := os.WriteFile(path, []byte(r.report()), 0o600); err != nil {
		return "", fmt.Errorf("chatlab: report: %w", err)
	}
	return path, nil
}
