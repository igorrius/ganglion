//go:build lab

package chatlab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router/rules"
)

func scripted(name string, script []ScriptStep, steps ...Step) Scenario {
	return Scenario{Name: name, Why: "w", Repeat: 1, Persona: PersonaDefault, Model: ModelScripted, Script: script, Steps: steps}
}

func runOne(t *testing.T, o RunOptions) Outcome {
	t.Helper()
	r := RunAll(context.Background(), o)
	if len(r.Outcomes) != len(o.Scenarios) {
		t.Fatalf("outcomes %+v", r.Outcomes)
	}
	return r.Outcomes[0]
}

var sayHello = []ScriptStep{{Say: "Hello."}, {Say: "Hello."}, {Say: "Hello."}, {Say: "Hello."}}

func TestRunPassesAndCountsRepeats(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Hello."}}})
	s.Repeat = 3
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}})
	if o.Result != ResultPassed || len(o.Repeats) != 3 || !strings.Contains(o.Facts, "3 of 3") {
		t.Fatalf("outcome %+v", o)
	}
	if !strings.Contains(o.Repeats[0].Transcript, "**owner:** hi") || !strings.Contains(o.Repeats[0].Transcript, "**bot:** Hello.") {
		t.Fatalf("transcript %q", o.Repeats[0].Transcript)
	}
}

func TestRepeatOptionOverrides(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Hello."}}})
	s.Repeat = 5
	if o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Repeat: 1}); len(o.Repeats) != 1 {
		t.Fatalf("%d repeats", len(o.Repeats))
	}
}

func TestRunFailsOnAFact(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Goodbye."}}})
	if o := runOne(t, RunOptions{Scenarios: []Scenario{s}}); o.Result != ResultFailed || !strings.Contains(o.Facts, "Goodbye.") {
		t.Fatalf("outcome %+v", o)
	}
}

func TestPressMissingLabelFails(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Press: "Approve"}, Step{Facts: &Facts{Questions: new(0)}})
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}})
	if o.Result != ResultFailed || !strings.Contains(o.Facts, "Approve") {
		t.Fatalf("outcome %+v", o)
	}
}

func TestReachInconclusive(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"},
		Step{Reach: &Reach{Journal: JournalFact{Tool: "memory.write"}, Nudge: "Please remember that."}},
		Step{Press: "Approve"})
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}})
	if o.Result != ResultInconclusive || !strings.Contains(o.Facts, "memory.write") {
		t.Fatalf("outcome %+v", o)
	}
	tr := o.Repeats[0].Transcript
	if strings.Count(tr, "**owner:** Please remember that.") != 1 {
		t.Fatalf("the nudge was not said once: %q", tr)
	}
}

func TestReachWithNudgePasses(t *testing.T) {
	s := scripted("x", []ScriptStep{{Say: "Sure."}, {Tool: "memory.write", Args: `{"title":"Boiler","body":"80 litres."}`}, {Say: "Stored."}},
		Step{Say: "My boiler holds 80 litres"},
		Step{Reach: &Reach{Journal: JournalFact{Tool: "memory.write"}, Nudge: "Please remember that."}},
		Step{Facts: &Facts{Memory: &Text{Contains: "80 litres"}}})
	if o := runOne(t, RunOptions{Scenarios: []Scenario{s}}); o.Result != ResultPassed {
		t.Fatalf("outcome %+v", o)
	}
}

func TestAnotherModelMakesItInconclusive(t *testing.T) {
	smart := providertest.NewServer(t, "a")
	cheap := providertest.NewServer(t, "b")
	smart.Fail(429, 60)
	cheap.Reply("Hello from the small model.")
	cat, err := rules.LoadCatalog(strings.NewReader(`providers:
  - {name: a, type: openai, base_url: "` + smart.URL() + `"}
  - {name: b, type: openai, base_url: "` + cheap.URL() + `"}
models:
  - {provider: a, name: big, tier: smart, tools: true, context: 200000}
  - {provider: b, name: small, tier: cheap, tools: true, context: 200000}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := Scenario{Name: "x", Why: "w", Repeat: 1, Persona: PersonaDefault, Steps: []Step{{Say: "hi"}, {Facts: &Facts{Reply: &Text{Contains: "Hello"}}}}}
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Catalog: cat, Keys: map[string]string{"a": "a", "b": "b"}})
	if o.Result != ResultInconclusive || !strings.Contains(o.Facts, "small") || !strings.Contains(o.Facts, "big") {
		t.Fatalf("outcome %+v", o)
	}
}

func TestNodeNeverQuiet(t *testing.T) {
	hung := providertest.NewServer(t, "a")
	hung.Hang()
	cat, err := rules.LoadCatalog(strings.NewReader(`providers:
  - {name: a, type: openai, base_url: "` + hung.URL() + `"}
models:
  - {provider: a, name: big, tier: smart, tools: true, context: 200000}
`))
	if err != nil {
		t.Fatal(err)
	}
	stuck := Scenario{Name: "stuck", Why: "w", Repeat: 1, Persona: PersonaDefault, Steps: []Step{{Say: "hi"}, {Facts: &Facts{Questions: new(0)}}}}
	next := scripted("next", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Hello."}}})
	r := RunAll(context.Background(), RunOptions{Scenarios: []Scenario{stuck, next}, Catalog: cat, Keys: map[string]string{"a": "a"}, TurnLimit: 2 * time.Second})
	if r.Outcomes[0].Result != ResultFailed || !strings.Contains(r.Outcomes[0].Facts, "not quiet") {
		t.Fatalf("stuck %+v", r.Outcomes[0])
	}
	if r.Outcomes[1].Result != ResultPassed {
		t.Fatalf("next %+v", r.Outcomes[1])
	}
}

func TestTokenCap(t *testing.T) {
	a := scripted("a", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Hello."}}})
	b := scripted("b", sayHello, Step{Say: "hi"}, Step{Facts: &Facts{Reply: &Text{Contains: "Hello."}}})
	r := RunAll(context.Background(), RunOptions{Scenarios: []Scenario{a, b}, MaxTokens: 1})
	if r.Outcomes[0].Result != ResultPassed || r.Outcomes[1].Result != ResultNotRun || !strings.Contains(r.Outcomes[1].Facts, "token") {
		t.Fatalf("outcomes %+v", r.Outcomes)
	}
	if r.Tokens == 0 {
		t.Fatal("no tokens in the run")
	}
}

func TestNoJudgeIsNotRun(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Judge: "It says hello."})
	if o := runOne(t, RunOptions{Scenarios: []Scenario{s}}); o.Result != ResultNotRun {
		t.Fatalf("outcome %+v", o)
	}
}

func TestJudgeThatDoesNotAnswerIsNotRun(t *testing.T) {
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Judge: "It says hello."})
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Judge: NewJudge(&recordingModel{err: context.DeadlineExceeded}, "m")})
	if o.Result != ResultNotRun {
		t.Fatalf("outcome %+v", o)
	}
}

func TestJudgeReadsOnlyTheModelsWords(t *testing.T) {
	m := &recordingModel{answer: `{"ok":true,"reason":""}`}
	s := scripted("x", []ScriptStep{
		{Tool: "schedule.create", Args: reminderArgs(t, time.Now().Add(time.Hour))}, {Say: "I asked."}, {Say: "It is set."},
	}, Step{Say: "Set a reminder"}, Step{Judge: "first"}, Step{Press: "Approve"}, Step{Judge: "second"})
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Judge: NewJudge(m, "m")})
	if o.Result != ResultPassed || len(m.got) != 2 {
		t.Fatalf("outcome %+v, %d judge calls", o, len(m.got))
	}
	first, second := m.got[0].Messages[1].Content, m.got[1].Messages[1].Content
	if !strings.Contains(first, "I asked.") || strings.Contains(first, "Create a reminder") {
		t.Errorf("first read %q", first)
	}
	if !strings.Contains(second, "It is set.") || strings.Contains(second, "Create a reminder") || strings.Contains(second, "I asked.") {
		t.Errorf("second read %q", second)
	}
}

func TestJudgeWithNothingToReadFails(t *testing.T) {
	m := &recordingModel{answer: `{"ok":true,"reason":""}`}
	s := scripted("x", sayHello, Step{Say: "hi"}, Step{Judge: "first"}, Step{Advance: &Advance{Confirmations: time.Minute}}, Step{Judge: "second"})
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Judge: NewJudge(m, "m")})
	if o.Result != ResultFailed || len(m.got) != 1 {
		t.Fatalf("outcome %+v, %d judge calls", o, len(m.got))
	}
}

func TestMissingRelayIsNotRun(t *testing.T) {
	cat, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: c, type: claude-cli}\nmodels:\n  - {provider: c, name: sonnet, tier: smart, tools: true, context: 200000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := Scenario{Name: "x", Why: "w", Repeat: 1, Persona: PersonaDefault, Steps: []Step{{Say: "hi"}, {Facts: &Facts{Questions: new(0)}}}}
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Catalog: cat, Relay: "/nonexistent/ganglion"})
	if o.Result != ResultNotRun || !strings.Contains(o.Facts, "ganglion") {
		t.Fatalf("outcome %+v", o)
	}
}

// A fact that fails under another model says nothing about the main one.
func TestAFactFailedUnderAnotherModelIsInconclusive(t *testing.T) {
	smart := providertest.NewServer(t, "a")
	cheap := providertest.NewServer(t, "b")
	smart.Fail(429, 60)
	cheap.Reply("Hello from the small model.")
	cat, err := rules.LoadCatalog(strings.NewReader(`providers:
  - {name: a, type: openai, base_url: "` + smart.URL() + `"}
  - {name: b, type: openai, base_url: "` + cheap.URL() + `"}
models:
  - {provider: a, name: big, tier: smart, tools: true, context: 200000}
  - {provider: b, name: small, tier: cheap, tools: true, context: 200000}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := Scenario{Name: "x", Why: "w", Repeat: 1, Persona: PersonaDefault, Steps: []Step{{Say: "hi"}, {Facts: &Facts{Reply: &Text{Contains: "Goodbye"}}}}}
	o := runOne(t, RunOptions{Scenarios: []Scenario{s}, Catalog: cat, Keys: map[string]string{"a": "a", "b": "b"}})
	if o.Result != ResultInconclusive || !strings.Contains(o.Facts, "small") {
		t.Fatalf("outcome %+v", o)
	}
	if !strings.Contains(o.Repeats[0].Transcript, "Goodbye") {
		t.Fatalf("the failure left the transcript: %q", o.Repeats[0].Transcript)
	}
}

// A first reply in the wrong language must not pass because the nudge's reply
// was in the right one.
func TestJudgeReadsTheReplyBeforeANudge(t *testing.T) {
	m := &recordingModel{answer: `{"ok":true,"reason":""}`}
	s := scripted("x", []ScriptStep{{Say: "First reply."}, {Tool: "memory.write", Args: `{"title":"Boiler","body":"80 litres."}`}, {Say: "Second reply."}},
		Step{Say: "My boiler holds 80 litres"},
		Step{Reach: &Reach{Journal: JournalFact{Tool: "memory.write"}, Nudge: "Please remember that."}},
		Step{Judge: "claim"})
	runOne(t, RunOptions{Scenarios: []Scenario{s}, Judge: NewJudge(m, "m")})
	if len(m.got) != 1 {
		t.Fatalf("%d judge calls", len(m.got))
	}
	read := m.got[0].Messages[1].Content
	if !strings.Contains(read, "First reply.") || !strings.Contains(read, "Second reply.") {
		t.Fatalf("the judge read %q", read)
	}
}

// The module sweep must stay inside the node's directory: with no ModulesDir it
// ran on ./html2md, and would delete the files of modules/html2md when run there.
func TestNodeLeavesTheWorkingDirectoryAlone(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	persona := t.TempDir()
	if err := copyDir(filepath.Join(root, "examples", "workspace", "persona"), filepath.Join(persona, "persona")); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	keep := filepath.Join(cwd, "html2md", "embed.go")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("package html2md"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	s := Scenario{Name: "x", Why: "w", Repeat: 1, Dir: persona, Persona: "persona", Model: ModelScripted, Script: sayHello,
		Steps: []Step{{Say: "hi"}, {Facts: &Facts{Reply: &Text{Contains: "Hello."}}}}}
	if o := runOne(t, RunOptions{Scenarios: []Scenario{s}}); o.Result != ResultPassed {
		t.Fatalf("outcome %+v", o)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the node removed a file of its working directory: %v", err)
	}
}

// ADR-0048: a key that a reply carries does not reach a report.
func TestAKeyInAReplyDoesNotReachTheReport(t *testing.T) {
	const key = "sk-or-v1-1234567890abcdef"
	s := scripted("x", []ScriptStep{{Say: "The page said " + key}}, Step{Say: "hi"}, Step{Facts: &Facts{Questions: new(0)}})
	r := RunAll(context.Background(), RunOptions{Scenarios: []Scenario{s}, Keys: map[string]string{"openrouter": key}})
	path, err := WriteReport(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "The page said") || strings.Contains(string(b), key) || strings.Contains(r.Table(), key) {
		t.Fatalf("report %s", b)
	}
}

// The scenarios on a scripted model spend nothing, so they run in every test run.
func TestShippedScriptedScenariosPass(t *testing.T) {
	all, err := LoadScenarios("../../tools/chatlab/scenarios", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mine []Scenario
	for _, s := range all {
		if s.Model == ModelScripted {
			mine = append(mine, s)
		}
	}
	if len(mine) == 0 {
		t.Fatal("no scripted scenarios")
	}
	for _, o := range RunAll(context.Background(), RunOptions{Scenarios: mine}).Outcomes {
		if o.Result != ResultPassed {
			t.Errorf("%s: %+v", o.Scenario, o)
		}
	}
}
