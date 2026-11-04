package chatlab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleRun() Run {
	return Run{
		Started: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		Tokens:  1234,
		Outcomes: []Outcome{
			Decide("language-ru", "the reply came in English", []Repeat{
				{Judgments: []Judgment{{Claim: "Russian", OK: true}}, Transcript: "**owner:** привет", Tokens: 10},
				{Judgments: []Judgment{{Claim: "Russian", OK: false, Reason: "it is English"}}, Transcript: "**bot:** hello"},
			}),
			Decide("r38", "a second question", []Repeat{{Failures: []string{"questions: wanted 1 showing, found 2"}, Transcript: "**bot:** a | b"}}),
		},
	}
}

func TestTableShowsNOfN(t *testing.T) {
	table := sampleRun().Table()
	for _, want := range []string{"| Scenario | Result | Facts | Meaning | Details |", "language-ru", "🟡 flaky", "Russian 1 of 2", "❌ failed", "1 failed", "1 flaky", "1234"} {
		if !strings.Contains(table, want) {
			t.Errorf("no %q in:\n%s", want, table)
		}
	}
}

func TestTableKeepsItsShape(t *testing.T) {
	r := Run{Outcomes: []Outcome{{Scenario: "s", Result: ResultFailed, Facts: "a | b\nc"}}}
	for line := range strings.SplitSeq(r.Table(), "\n") {
		if strings.HasPrefix(line, "| s ") && strings.Count(line, " | ") != 4 {
			t.Fatalf("a cell broke the row: %q", line)
		}
	}
}

func TestReportHoldsTheDetails(t *testing.T) {
	path, err := WriteReport(t.TempDir(), sampleRun())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(path)) != "20261104-120000" || filepath.Base(path) != "report.md" {
		t.Fatalf("path %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the reply came in English", "it is English", "**owner:** привет", "found 2"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %q in the report", want)
		}
	}
}

func TestReportRedactsKeys(t *testing.T) {
	const key = "sk-or-v1-1234567890abcdef"
	const bot = "7001234567:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	r := sampleRun()
	r.Secrets = []string{key}
	r.Outcomes[0].Repeats[0].Transcript = "the page said " + key + " and " + bot
	r.Outcomes[0].Repeats[1].Judgments[0].Reason = "it quotes " + key
	r.Outcomes[1].Facts = "failure with " + key
	path, err := WriteReport(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(b), r.Table()} {
		if strings.Contains(text, key) || strings.Contains(text, bot) {
			t.Fatalf("a key reached the report:\n%s", text)
		}
	}
}

func TestReportFileModes(t *testing.T) {
	path, err := WriteReport(t.TempDir(), sampleRun())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatalf("file %v, directory %v", file.Mode(), dir.Mode())
	}
}
