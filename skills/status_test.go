package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

func TestProblemKinds(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "skills", "a-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "skills", "no-md"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, ws, "other-name", "name: not-the-dir\ndescription: x\n", "")
	dir := writeSkill(t, ws, "not-exec", "name: not-exec\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n", "echo\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, ws, "metrics", "name: metrics\ndescription: mine\n", "")
	_, problems, err := Load(builtinFS, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ProblemKind{"a-file": ProblemShape, "no-md": ProblemRead, "other-name": ProblemFrontmatter,
		"not-exec": ProblemScript, "metrics": ProblemBuiltinName}
	if len(problems) != len(want) {
		t.Fatalf("problems:\n%s", reasons(problems))
	}
	for _, p := range problems {
		if p.Kind != want[p.Skill] {
			t.Errorf("%s: kind %q, want %q (%s)", p.Skill, p.Kind, want[p.Skill], p.Reason)
		}
	}
}

func TestRefusedStatus(t *testing.T) {
	since := time.Date(2026, 11, 8, 9, 15, 0, 0, time.UTC)
	r := Refused{Loaded: 3, Since: since, Problems: []Problem{{Skill: "rates", Reason: "script.run \"x\" is not executable", Kind: ProblemScript}}}
	cs := r.Status(t.Context(), status.Scope{Now: since.Add(time.Hour)})
	if len(cs) != 2 {
		t.Fatalf("lines %+v", cs)
	}
	if c := cs[0]; c.State != status.OK || c.Line != texts.StatusSkills || c.Vars["n"] != "3" {
		t.Errorf("loaded: %+v", c)
	}
	c := cs[1]
	if c.State != status.Warn || c.Line != texts.StatusSkillRefused || c.Reason != texts.StatusReasonSkillScript ||
		c.Vars["skill"] != "rates" || !c.Since.Equal(since) || c.Section != status.SectionProblems {
		t.Errorf("refused: %+v", c)
	}
	for _, v := range c.Vars {
		if v == r.Problems[0].Reason {
			t.Error("the loader's text reached a line")
		}
	}
}
