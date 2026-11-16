package builtin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// writeExamples puts an examples file into the reader's source.
func (e *moduleEnv) writeExamples(t *testing.T, data []byte) {
	t.Helper()
	p := filepath.Join(e.m.Workspace.Dir(), "modules", "demo-reader", modtrial.FileName)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

var failedTrial = modtrial.Report{Examples: []modtrial.ExampleResult{{Name: "charges", Steps: []modtrial.StepResult{{N: 2, Ran: true, Mismatch: "expected 1 message, got 0"}}}}}

func answerOf(t *testing.T, e *moduleEnv, art string) map[string]any {
	t.Helper()
	return e.build(t, artifact(art, 1024, 1))
}

// A build with examples is tried; a failing trial refuses the plan with the
// node's words, which are also the trial line, and the report is kept
// beside the build.
func TestModuleBuildRefusesAFailedTrial(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.writeExamples(t, []byte("examples: []\n"))
	e.reloads.trial = failedTrial
	ans := answerOf(t, e, demoManifest)
	line := failedTrial.ModelLine()
	if ans["refusal"] != "the trial run failed" || ans["refusal_detail"] != line || ans["trial"] != line {
		t.Fatalf("answer %v", ans)
	}
	if next, _ := ans["next"].(string); !strings.Contains(next, "examples.yaml") {
		t.Fatalf("next %q", next)
	}
	if len(e.reloads.trialFiles) != 1 || string(e.reloads.trialFiles[0]) != "examples: []\n" {
		t.Fatalf("trial files %q", e.reloads.trialFiles)
	}
	sha, _ := ans["build"].(string)
	if _, err := os.Stat(filepath.Join(e.m.Builds, sha+".trial.json")); err != nil {
		t.Fatalf("the trial is not kept beside the build: %v", err)
	}
	// The install refuses before asking.
	_, err := e.tool(builtin.ModuleInstallTool).(tool.Questioner).Question(context.Background(), installCall(sha))
	if err == nil || !strings.Contains(err.Error(), "the trial run failed") {
		t.Fatalf("question err %v", err)
	}
}

// A passing trial leaves the plan alone and shows its line.
func TestModuleBuildShowsAPassedTrial(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.writeExamples(t, []byte("examples: []\n"))
	e.reloads.trial = modtrial.Report{Passed: true, Examples: []modtrial.ExampleResult{{Name: "a", Passed: true}}}
	ans := answerOf(t, e, demoManifest)
	if ans["refusal"] != nil || ans["trial"] != "trial: 1 example, all passed" {
		t.Fatalf("answer %v", ans)
	}
}

// No examples file: no trial, no stored report, and the install plans as it
// always did.
func TestModuleBuildWithoutExamplesIsNotTried(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	art := artifact(demoManifest, 1024, 1)
	ans := e.build(t, art)
	if ans["trial"] != "no examples.yaml: the module was not tried" || ans["refusal"] != nil {
		t.Fatalf("answer %v", ans)
	}
	if len(e.reloads.trialFiles) != 0 {
		t.Fatal("a build with no examples was tried")
	}
	if _, err := os.Stat(filepath.Join(e.m.Builds, art.SHA256+".trial.json")); err == nil {
		t.Fatal("a trial was stored for a build with no examples")
	}
	c := installCall(art.SHA256)
	question(t, e, c)
	if _, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c)); err != nil {
		t.Fatal(err)
	}
}

// An examples file over the limit is a refused report; the host is not asked.
func TestModuleBuildRefusesATooBigExamplesFile(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.writeExamples(t, []byte(strings.Repeat("#", modtrial.MaxFileBytes+1)))
	ans := e.build(t, artifact(demoManifest, 1024, 1))
	if ans["refusal"] != "the trial run failed" || !strings.Contains(ans["trial"].(string), "examples.yaml refused") || len(e.reloads.trialFiles) != 0 {
		t.Fatalf("answer %v files %d", ans, len(e.reloads.trialFiles))
	}
}

// An examples file that is not a regular file (a link to elsewhere) is
// refused, never read.
func TestModuleBuildRefusesALinkedExamplesFile(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("examples: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(e.m.Workspace.Dir(), "modules", "demo-reader", modtrial.FileName)); err != nil {
		t.Fatal(err)
	}
	ans := e.build(t, artifact(demoManifest, 1024, 1))
	if ans["refusal"] != "the trial run failed" || len(e.reloads.trialFiles) != 0 {
		t.Fatalf("answer %v", ans)
	}
}

// A trial that cannot run ends the build job in the node's words.
func TestModuleBuildTrialErrorEndsTheJob(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.writeExamples(t, []byte("examples: []\n"))
	e.reloads.trialErr = errors.New("the build did not pass its check: it does not load on this node")
	e.b.art = artifact(demoManifest, 1024, 1)
	res := e.buildNow(t, `{"name":"demo-reader"}`)
	if !strings.Contains(res.Text, "it does not load on this node") || strings.Contains(res.Text, `"build"`) {
		t.Fatalf("answer %s", res.Text)
	}
}
