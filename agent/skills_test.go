package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/tool/builtin"
)

func skillEnv(t *testing.T) *confirmEnv {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, "skills", "quick-sum")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: quick-sum\ndescription: Add numbers fast\nmodel:\n  tier: cheap\n---\nAdd them.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, problems, err := skills.Load(skills.Builtin, ws, nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", err, problems)
	}
	e := newEnv(t, Config{}, nil)
	e.deps.Skills = set
	return newConfirmEnvOn(t, e, builtin.NewSkillTools(set, skills.NewRunner(skills.DefaultRunConfig()))...)
}

// R-RTR-4: after the model reads a skill, the rest of the turn is routed
// by the skill's model block.
func TestSkillReadRoutesByModel(t *testing.T) {
	e := skillEnv(t)
	e.smart.push(callWith("skill.read", `{"name":"quick-sum"}`))
	e.cheap.push(say("5"))
	out := e.turn(t, message(owner, "Igor", "add 2 and 3"))
	if len(e.smart.requests()) != 1 || len(e.cheap.requests()) != 1 {
		t.Fatalf("smart %d, cheap %d requests", len(e.smart.requests()), len(e.cheap.requests()))
	}
	if len(out) != 1 || out[0].Text != "5" {
		t.Fatalf("out %+v", out)
	}
}

// The skills index is in an admin's system prompt, the same bytes every
// turn; a family member's prompt has none.
func TestSkillsIndexStable(t *testing.T) {
	e := skillEnv(t)
	e.smart.push(say("a"), say("b"))
	e.turn(t, message(owner, "Igor", "one"))
	e.turn(t, message(owner, "Igor", "two"))
	reqs := e.smart.requests()
	p0, p1 := reqs[0].Messages[0].Content, reqs[1].Messages[0].Content
	if p0 != p1 || !strings.Contains(p0, "- quick-sum: Add numbers fast") || !strings.Contains(p0, "- metrics: ") {
		t.Fatalf("prompts differ or lack the index:\n%s", p0)
	}
	e.admin = false
	e.smart.push(say("c"))
	e.turn(t, message(owner, "Igor", "three"))
	if fam := e.smart.requests()[2].Messages[0].Content; strings.Contains(fam, "quick-sum") {
		t.Fatal("a family member's prompt lists the skills")
	}
}

func TestSkillRouteKeepsLocalOnly(t *testing.T) {
	a := &Agent{d: Deps{Skills: fakeSkillSet{"private": {Model: router.TaskReq{Privacy: skills.PrivacyLocalOnly}},
		"cloudy": {Model: router.TaskReq{Tier: router.TierSmart}}}}}
	task := a.skillRoute(router.TaskReq{}, []provider.ToolCall{readCall("private"), readCall("cloudy")}, true)
	if task.Privacy != skills.PrivacyLocalOnly || task.Tier != router.TierSmart {
		t.Fatalf("task %+v", task)
	}
	if task := a.skillRoute(router.TaskReq{}, []provider.ToolCall{readCall("cloudy")}, false); task.Tier != "" {
		t.Fatalf("a family turn was re-routed: %+v", task)
	}
}

type fakeSkillSet map[string]skills.Skill

func (f fakeSkillSet) Index() string                        { return "" }
func (f fakeSkillSet) Get(name string) (skills.Skill, bool) { s, ok := f[name]; return s, ok }
func readCall(name string) provider.ToolCall {
	return provider.ToolCall{Name: "skill.read", Args: []byte(`{"name":"` + name + `"}`)}
}

func TestCoreRulesSayHowToProposeASkill(t *testing.T) {
	if !strings.Contains(CoreRules, "propose a skill") {
		t.Fatal("the core rules do not say the agent may propose a skill")
	}
}
