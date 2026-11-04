package chatlab

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/internal/testinputs"
)

// changedRepo is a small repository: one source file, one shared file, and a
// scenario that covers the first.
func changedRepo(t *testing.T) (root string, s Scenario) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("agent/scheduled.go", "package agent")
	write("agent/other.go", "package agent")
	write("agent/prompt.go", "package agent")
	write("scenarios/pages/tips.html", "tips")
	write("scenarios/x.yaml", "name: x\nwhy: y\ncovers: [agent/scheduled.go]\nsteps:\n  - judge: holds\n")
	s, err := LoadScenario(filepath.Join(root, "scenarios", "x.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return root, s
}

func rewrite(t *testing.T, root, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func selected(t *testing.T, root string, s Scenario, rec Record) bool {
	t.Helper()
	run, _, err := Select(root, []Scenario{s}, rec)
	if err != nil {
		t.Fatal(err)
	}
	return len(run) == 1
}

func passedNow(t *testing.T, root string, s Scenario) Record {
	t.Helper()
	rec := Record{}
	if err := rec.Passed(root, s); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestANewScenarioRuns(t *testing.T) {
	root, s := changedRepo(t)
	if !selected(t, root, s, Record{}) {
		t.Fatal("a scenario with no green run was left out")
	}
}

func TestAnUntouchedScenarioDoesNotRun(t *testing.T) {
	root, s := changedRepo(t)
	if selected(t, root, s, passedNow(t, root, s)) {
		t.Fatal("nothing changed, and it runs")
	}
}

func TestAChangedCoveredFileRuns(t *testing.T) {
	root, s := changedRepo(t)
	rec := passedNow(t, root, s)
	rewrite(t, root, "agent/scheduled.go", "package agent // changed")
	if !selected(t, root, s, rec) {
		t.Fatal("its file changed, and it did not run")
	}
}

func TestAnotherFileDoesNotRunIt(t *testing.T) {
	root, s := changedRepo(t)
	rec := passedNow(t, root, s)
	rewrite(t, root, "agent/other.go", "package agent // changed")
	if selected(t, root, s, rec) {
		t.Fatal("a file it does not cover ran it")
	}
}

func TestChangingTheScenarioRunsIt(t *testing.T) {
	root, s := changedRepo(t)
	rec := passedNow(t, root, s)
	rewrite(t, root, "scenarios/x.yaml", "name: x\nwhy: changed\ncovers: [agent/scheduled.go]\nsteps:\n  - judge: holds\n")
	s2, err := LoadScenario(filepath.Join(root, "scenarios", "x.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !selected(t, root, s2, rec) {
		t.Fatal("its own file changed, and it did not run")
	}
}

func TestChangingItsPageRunsIt(t *testing.T) {
	root, s := changedRepo(t)
	s.Pages = []Page{{URL: "http://a.example.net/", File: "pages/tips.html"}}
	rec := passedNow(t, root, s)
	rewrite(t, root, "scenarios/pages/tips.html", "new tips")
	if !selected(t, root, s, rec) {
		t.Fatal("its page changed, and it did not run")
	}
}

func TestAFailedScenarioRunsAgain(t *testing.T) {
	root, s := changedRepo(t)
	rec := passedNow(t, root, s)
	rec.Failed(s)
	if !selected(t, root, s, rec) {
		t.Fatal("a scenario that failed was left out")
	}
}

func TestSelectSaysWhatItLeftOut(t *testing.T) {
	root, s := changedRepo(t)
	run, untouched, err := Select(root, []Scenario{s}, passedNow(t, root, s))
	if err != nil || len(run) != 0 || !slices.Equal(untouched, []string{"x"}) {
		t.Fatalf("run %v, untouched %v, %v", run, untouched, err)
	}
}

func TestRecordSurvivesItsFile(t *testing.T) {
	root, s := changedRepo(t)
	path := filepath.Join(t.TempDir(), "lab", "passed.json")
	rec, err := LoadRecord(path)
	if err != nil || len(rec.Scenarios) != 0 {
		t.Fatalf("no file yet: %+v, %v", rec, err)
	}
	if err := rec.Passed(root, s); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if selected(t, root, s, back) {
		t.Fatal("the record did not survive")
	}
}

func TestABrokenRecordRunsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passed.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadRecord(path)
	if err != nil || len(rec.Scenarios) != 0 {
		t.Fatalf("record %+v, %v", rec, err)
	}
}

func TestACoversThatMatchesNothingIsAnError(t *testing.T) {
	root, s := changedRepo(t)
	s.Covers = []string{"agent/no-such-file.go"}
	if _, _, err := Select(root, []Scenario{s}, Record{}); err == nil || !strings.Contains(err.Error(), "no-such-file") {
		t.Fatalf("err %v", err)
	}
}

func TestFullRunReasons(t *testing.T) {
	root, _ := changedRepo(t)
	shared := []string{"agent/prompt.go"}
	rec := Record{}
	reasons, err := rec.FullRunReasons(root, shared)
	if err != nil || !slices.Equal(reasons, []string{"no full run yet"}) {
		t.Fatalf("first: %v, %v", reasons, err)
	}
	if err := rec.FullRunDone(root, shared); err != nil {
		t.Fatal(err)
	}
	if reasons, err := rec.FullRunReasons(root, shared); err != nil || len(reasons) != 0 {
		t.Fatalf("after a full run: %v, %v", reasons, err)
	}
	rewrite(t, root, "agent/prompt.go", "package agent // changed")
	reasons, err = rec.FullRunReasons(root, shared)
	if err != nil || !slices.Equal(reasons, []string{"agent/prompt.go"}) {
		t.Fatalf("changed: %v, %v", reasons, err)
	}
	// Deciding to skip it clears the recommendation, as a full run does.
	if err := rec.FullRunDone(root, shared); err != nil {
		t.Fatal(err)
	}
	if reasons, _ := rec.FullRunReasons(root, shared); len(reasons) != 0 {
		t.Fatalf("after deciding: %v", reasons)
	}
}

func TestTableSaysWhatDidNotRun(t *testing.T) {
	r := Run{Outcomes: []Outcome{Decide("a", "w", []Repeat{{}})}, Untouched: []string{"b", "c"}, FullRun: []string{"agent/prompt.go"}}
	table := r.Table()
	for _, want := range []string{"1 ran, 2 untouched", "Full run recommended", "agent/prompt.go", "make lab-all"} {
		if !strings.Contains(table, want) {
			t.Errorf("no %q in:\n%s", want, table)
		}
	}
	if strings.Contains(table, "| b |") {
		t.Errorf("an untouched scenario got a row:\n%s", table)
	}
}

func TestAPureUntouchedRunExitsZero(t *testing.T) {
	if got := ExitCode(nil, []string{"a"}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if got := ExitCode(nil, nil); got != 2 {
		t.Fatalf("no scenarios at all: exit %d", got)
	}
}

const repoRoot = "../.."

func shippedScenarios(t *testing.T) []Scenario {
	t.Helper()
	all, err := LoadScenarios(filepath.Join(repoRoot, "tools", "chatlab", "scenarios"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestShippedScenariosCoverRealFiles(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range shippedScenarios(t) {
		if len(s.Covers) == 0 {
			t.Errorf("%s covers nothing, so a change to the bot would never run it", s.Name)
		}
		if _, err := fingerprint(root, s); err != nil {
			t.Error(err)
		}
	}
}

// Every file of the binary is either covered by a scenario, shared, or listed as
// outside what chat scenarios check. So a new file cannot silently fall out of
// `make lab`.
func TestEveryProductionFileIsClaimed(t *testing.T) {
	testinputs.Build(t, "../..")
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	claimed := slices.Clone(SharedFiles)
	for _, s := range shippedScenarios(t) {
		claimed = append(claimed, s.Covers...)
	}
	list, err := os.ReadFile(filepath.Join(root, "tools", "chatlab", "no-scenario.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(list), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			claimed = append(claimed, line)
		}
	}
	got := map[string]bool{}
	for _, pattern := range claimed {
		files, err := matchFiles(root, pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Errorf("%q matches no file", pattern)
		}
		for _, f := range files {
			got[f] = true
		}
	}
	cmd := exec.Command("go", "list", "-deps", "-f", `{{if not .Standard}}{{.Dir}}{{range .GoFiles}} {{.}}{{end}}{{end}}`, ".")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], root) {
			continue // another module's
		}
		rel, err := filepath.Rel(root, fields[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fields[1:] {
			if path := filepath.ToSlash(filepath.Join(rel, f)); !got[path] {
				missing = append(missing, path)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("name these in a scenario's covers, or in tools/chatlab/no-scenario.txt:\n%s", strings.Join(missing, "\n"))
	}
}

func TestAnEmptyRecordIsEmpty(t *testing.T) {
	root, s := changedRepo(t)
	if !(Record{}).Empty() {
		t.Fatal("no scenarios, and not empty")
	}
	if passedNow(t, root, s).Empty() {
		t.Fatal("one scenario, and empty")
	}
}
