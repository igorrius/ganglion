package agent

import (
	"context"
	"github.com/igorrius/ganglion/router/rules"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	return newConfirmEnvOn(t, e, builtin.NewSkillTools(set, skills.NewRunner(skills.DefaultRunConfig()), noRuns{})...)
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

// noRuns lets every change script run.
type noRuns struct{}

func (noRuns) Begin(context.Context, string, string) (bool, error) { return true, nil }

// localEnv has a local model beside the cloud ones and a local-only skill.
func localEnv(t *testing.T, cfg Config) (*confirmEnv, *fakeProvider) {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, "skills", "private")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: private\ndescription: Household accounts\nmodel:\n  privacy: local-only\n---\nRead the accounts.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, problems, err := skills.Load(skills.Builtin, ws, nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", err, problems)
	}
	e := newEnv(t, cfg, nil)
	local := &fakeProvider{name: "p3"}
	e.deps.Providers["p3"] = local
	e.deps.Router, err = rules.New([]router.Model{
		{Provider: "p1", Name: "smart", Tier: "smart", Caps: router.ModelCaps{Tools: true, PriceInMTok: 1, PriceOutMTok: 1}},
		{Provider: "p2", Name: "cheap", Tier: "cheap", Caps: router.ModelCaps{Tools: true}},
		// Dearer, so only a local-only task picks it.
		{Provider: "p3", Name: "local", Tier: "smart", Caps: router.ModelCaps{Tools: true, Local: true, PriceInMTok: 50, PriceOutMTok: 50}},
	}, rules.WithClock(func() time.Time { return e.now }))
	if err != nil {
		t.Fatal(err)
	}
	e.deps.Skills = set
	return newConfirmEnvOn(t, e, builtin.NewSkillTools(set, skills.NewRunner(skills.DefaultRunConfig()), noRuns{})...), local
}

// Spec 7.3: "data never leaves for the cloud". A local-only skill's text joins
// the history, so the next turn must stay local too, until the session ends.
func TestLocalOnlyLastsForTheSession(t *testing.T) {
	e, local := localEnv(t, Config{})
	e.smart.push(callWith("skill.read", `{"name":"private"}`))
	local.push(say("Accounts read."))
	e.turn(t, message(owner, "Igor", "how are my accounts?"))
	cloud := len(e.smart.requests())

	local.push(say("You are welcome."))
	if out := e.turn(t, message(owner, "Igor", "thanks")); len(out) != 1 || out[0].Text != "You are welcome." {
		t.Fatalf("out %+v", out)
	}
	if n := len(e.smart.requests()); n != cloud {
		t.Fatalf("the next turn went to the cloud: %d requests after %d", n, cloud)
	}

	e.turn(t, message(owner, "Igor", "/new"))
	e.smart.push(say("hello"))
	if out := e.turn(t, message(owner, "Igor", "hi")); len(out) != 1 || out[0].Text != "hello" {
		t.Fatalf("a new session stayed local: %+v", out)
	}
}

// Compaction reads the same history: it stays off the cloud too.
func TestCompactionStaysLocal(t *testing.T) {
	e, local := localEnv(t, Config{HistoryTokens: 60, KeepRecent: 4})
	long := strings.Repeat("слово ", 20)
	e.smart.push(callWith("skill.read", `{"name":"private"}`))
	local.push(say("one"), say("two"), say("three"))
	for range 3 {
		e.turn(t, message(owner, "Igor", long))
	}
	if n := len(local.requests()); n < 3 {
		t.Fatalf("the local model got %d requests", n)
	}
	if n := len(e.cheap.requests()); n != 0 {
		t.Fatalf("the cheap cloud model got %d compaction requests of a local-only session", n)
	}
}

// memory.recall feeds a later, maybe cloud, session: it leaves such a session out.
func TestRecallLeavesOutLocalOnlySessions(t *testing.T) {
	m := newMemoryEnv(t)
	private := State{V: StateVersion, Session: "s1", Started: t0, Last: t0, LocalOnly: true,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Igor: my salary is secret"}}}
	if err := m.archive.Put(context.Background(), "telegram:42", private, "new"); err != nil {
		t.Fatal(err)
	}
	got, err := m.archive.Search(context.Background(), "salary", []string{"telegram:42"}, 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("recall found %+v, %v", got, err)
	}
	if st, err := DecodeState(mustJSON(t, private)); err != nil || !st.LocalOnly {
		t.Fatalf("the state lost it: %+v, %v", st, err)
	}
}

func mustJSON(t *testing.T, s State) []byte {
	t.Helper()
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
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
