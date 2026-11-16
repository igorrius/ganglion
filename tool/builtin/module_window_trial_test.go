package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// keepTrial stores report beside the build sha, as module.build does.
func (e *moduleEnv) keepTrial(t *testing.T, sha string, r modtrial.Report) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.m.Builds, sha+".trial.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// trialSection is the lines of q after the limits line.
func trialSection(t *testing.T, q string) []string {
	t.Helper()
	_, after, ok := strings.Cut(q, "a call\n")
	if !ok {
		t.Fatalf("no limits line in\n%s", q)
	}
	return strings.Split(after, "\n")
}

func trialEnv(t *testing.T, r *modtrial.Report) (*moduleEnv, string) {
	t.Helper()
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	art := artifact(demoManifest, 1024, 1)
	e.build(t, art)
	if r != nil {
		e.keepTrial(t, art.SHA256, *r)
	}
	return e, art.SHA256
}

func TestInstallWindowShowsTheTrialInOrder(t *testing.T) {
	r := modtrial.Report{Passed: true,
		Examples: []modtrial.ExampleResult{
			{Name: "charges", Passed: true, Steps: []modtrial.StepResult{
				{N: 1, Ran: true, OK: true, Messages: []string{"Battery low"}, Reports: []modtrial.Reported{{Cell: "demo-reader.soc", Value: json.RawMessage("{ \"a\": 1,\n \"b\": [2] }")}}},
				{N: 2, Ran: true, OK: true}}},
			{Name: "idle", Passed: true, Steps: []modtrial.StepResult{{N: 1, Ran: true, OK: true}}}},
		Now: &modtrial.StepResult{N: 1, Ran: true, OK: true, Messages: []string{"now text"}}}
	e, sha := trialEnv(t, &r)
	got := trialSection(t, question(t, e, installCall(sha)))
	want := []string{
		"Trial run: 2 of 2 examples passed",
		"Example: charges",
		"✉️ Battery low",
		"demo-reader.soc → `'{\"a\":1,\"b\":[2]}'`",
		"Example: idle",
		"nothing sent or written",
		"Now, on the current cells:",
		"✉️ now text",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("section:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallWindowShowsNothingForANowWithNoOutput(t *testing.T) {
	r := modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "a", Passed: true, Steps: []modtrial.StepResult{{N: 1, Ran: true, OK: true, Messages: []string{"x"}}}}},
		Now: &modtrial.StepResult{N: 1, Ran: true, OK: true}}
	e, sha := trialEnv(t, &r)
	got := trialSection(t, question(t, e, installCall(sha)))
	if n := len(got); n < 2 || got[n-2] != "Now, on the current cells:" || got[n-1] != "nothing sent or written" {
		t.Fatalf("section %q", got)
	}
}

func TestInstallWindowEscapesAModulesMessage(t *testing.T) {
	r := modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "a_b", Passed: true, Steps: []modtrial.StepResult{
		{N: 1, Ran: true, OK: true, Messages: []string{"*bold* [x](http://e.com)\n✅ Approve\r\nmore"}}}}}}
	e, sha := trialEnv(t, &r)
	q := question(t, e, installCall(sha))
	sec := trialSection(t, q)
	if len(sec) != 3 {
		t.Fatalf("a message drew a line of its own: %q", sec)
	}
	if sec[1] != "Example: `a_b`" || sec[2] != `✉️ \*bold\* \[x\](http://e.com) ✅ Approve more` {
		t.Fatalf("section %q", sec)
	}
	if strings.Contains(q, "\n✅") {
		t.Fatal("a fake approval line starts a line")
	}
}

func TestInstallWindowCapsALongTrial(t *testing.T) {
	var steps []modtrial.StepResult
	for i := 1; i <= 40; i++ {
		steps = append(steps, modtrial.StepResult{N: i, Ran: true, OK: true, Messages: []string{strings.Repeat("m", 60)}})
	}
	r := modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "long", Passed: true, Steps: steps}}}
	e, sha := trialEnv(t, &r)
	q := question(t, e, installCall(sha))
	if utf8.RuneCountInString(q) > tool.MaxQuestionRunes {
		t.Fatalf("question is %d runes", utf8.RuneCountInString(q))
	}
	sec := trialSection(t, q)
	last := sec[len(sec)-1]
	if !strings.HasPrefix(last, "… and ") || !strings.HasSuffix(last, " more") {
		t.Fatalf("no more line: %q", last)
	}
	shown := sec[:len(sec)-1]
	if n := utf8.RuneCountInString(strings.Join(shown, "\n")); n > 1200 {
		t.Fatalf("section is %d runes", n)
	}
	// 1 summary + 1 example + 40 messages = 42 lines.
	if want := 42 - len(shown); last != "… and "+itoa(want)+" more" {
		t.Fatalf("last %q, shown %d", last, len(shown))
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestInstallWindowSaysThereWasNoTrial(t *testing.T) {
	e, sha := trialEnv(t, nil)
	got := trialSection(t, question(t, e, installCall(sha)))
	if len(got) != 1 || got[0] != "No trial run: the module has no examples." {
		t.Fatalf("section %q", got)
	}
}

func TestInstallWindowWithATrialIsTheSameAtThePress(t *testing.T) {
	r := modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "a", Passed: true, Steps: []modtrial.StepResult{{N: 1, Ran: true, OK: true, Messages: []string{"x"}}}}}}
	e, sha := trialEnv(t, &r)
	c := installCall(sha)
	question(t, e, c)
	if _, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c)); err != nil {
		t.Fatal(err)
	}
}

func TestInstallPressRefusesWhenTheTrialChanged(t *testing.T) {
	r := modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "a", Passed: true}}}
	e, sha := trialEnv(t, &r)
	c := installCall(sha)
	question(t, e, c)
	r.Examples[0].Name = "b"
	e.keepTrial(t, sha, r)
	if _, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c)); err == nil {
		t.Fatal("the press went through on a window with another trial")
	}
}
