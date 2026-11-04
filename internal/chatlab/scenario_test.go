package chatlab

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/schemas"
)

// writeScenario puts body in dir as name.yaml, with a page file beside it.
func writeScenario(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pages", "tips.html"), []byte("<p>tips</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The example of the design, section 5.4.
const r48Example = `name: r48-waiting-call-is-not-done
why: the model once said "I updated the record" while the write waited
repeat: 3
persona: default
pages:
  - {url: "http://solar-notes.example.net/tips", file: pages/tips.html}
steps:
  - say: "Read http://solar-notes.example.net/tips"
  - say: "Remember that my boiler holds 80 litres"
  - facts:
      journal: [{tool: memory.write, decision: confirm}]
      questions: 1
  - judge: "The last reply says the fact is not saved yet."
  - advance: {confirmations: 16m}
  - facts:
      journal: [{tool: memory.write, decision: expired}]
      message_replaced: "Expired"
`

func TestLoadScenario(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadScenario(writeScenario(t, dir, "r48-waiting-call-is-not-done", r48Example))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "r48-waiting-call-is-not-done" || s.Repeat != 3 || s.Persona != "default" || s.Dir != dir || len(s.Steps) != 6 {
		t.Fatalf("scenario %+v", s)
	}
	if s.Pages[0].URL != "http://solar-notes.example.net/tips" || s.Pages[0].File != "pages/tips.html" {
		t.Fatalf("pages %+v", s.Pages)
	}
	f := s.Steps[2].Facts
	if f == nil || f.Journal[0].Tool != "memory.write" || f.Journal[0].Decision != "confirm" || f.Questions == nil || *f.Questions != 1 {
		t.Fatalf("facts %+v", f)
	}
	if a := s.Steps[4].Advance; a == nil || a.Confirmations != 16*time.Minute {
		t.Fatalf("advance %+v", a)
	}
	if s.Steps[5].Facts.MessageReplaced != "Expired" {
		t.Fatalf("message_replaced %+v", s.Steps[5].Facts)
	}
}

func TestLoadScenarioDefaults(t *testing.T) {
	s, err := LoadScenario(writeScenario(t, t.TempDir(), "x", "name: x\nwhy: y\nsteps:\n  - judge: holds\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Repeat != 3 || s.Persona != "default" {
		t.Fatalf("defaults %+v", s)
	}
}

func TestLoadScenarioScripted(t *testing.T) {
	s, err := LoadScenario(writeScenario(t, t.TempDir(), "x", `name: x
why: a real model cannot be made to repeat a call
model: scripted
script:
  - {tool: schedule.create, args: '{"kind":"reminder"}'}
  - {say: asked}
steps:
  - say: go
  - facts: {questions: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Script) != 2 || s.Script[0].Tool != "schedule.create" || s.Script[1].Say != "asked" {
		t.Fatalf("script %+v", s.Script)
	}
}

func TestLoadScenarioRefuses(t *testing.T) {
	const ok = "steps:\n  - judge: holds\n"
	for _, tc := range []struct{ name, body, want string }{
		{"unknown field", "name: x\nwhy: y\nwat: 1\n" + ok, "wat"},
		{"two actions", "name: x\nwhy: y\nsteps:\n  - {say: a, press: b}\n  - judge: holds\n", "one action"},
		{"no action", "name: x\nwhy: y\nsteps:\n  - {}\n  - judge: holds\n", "one action"},
		{"empty facts", "name: x\nwhy: y\nsteps:\n  - facts: {}\n", "one action"},
		{"no why", "name: x\n" + ok, "why"},
		{"cannot fail", "name: x\nwhy: y\nsteps:\n  - say: hello\n", "cannot fail"},
		{"judge under scripted", "name: x\nwhy: y\nmodel: scripted\nscript: [{say: a}]\n" + ok, "judge"},
		{"scripted without script", "name: x\nwhy: y\nmodel: scripted\nsteps:\n  - facts: {questions: 1}\n", "script"},
		{"script without scripted", "name: x\nwhy: y\nscript: [{say: a}]\n" + ok, "script"},
		{"script step with both", "name: x\nwhy: y\nmodel: scripted\nscript: [{say: a, tool: b}]\nsteps:\n  - facts: {questions: 1}\n", "script"},
		{"unknown model", "name: x\nwhy: y\nmodel: gpt\n" + ok, "model"},
		{"page over https", "name: x\nwhy: y\npages: [{url: 'https://a.example.net/', file: pages/tips.html}]\n" + ok, "must be http"},
		{"page on loopback", "name: x\nwhy: y\npages: [{url: 'http://localhost/', file: pages/tips.html}]\n" + ok, "loopback"},
		{"page on an address", "name: x\nwhy: y\npages: [{url: 'http://127.0.0.1/', file: pages/tips.html}]\n" + ok, "loopback"},
		{"page escapes", "name: x\nwhy: y\npages: [{url: 'http://a.example.net/', file: ../secret}]\n" + ok, "outside"},
		{"page missing", "name: x\nwhy: y\npages: [{url: 'http://a.example.net/', file: pages/none.html}]\n" + ok, "none.html"},
		{"persona escapes", "name: x\nwhy: y\npersona: ../../workspace\n" + ok, "outside"},
		{"skill escapes", "name: x\nwhy: y\nskills: [../x]\n" + ok, "outside"},
		{"negative repeat", "name: x\nwhy: y\nrepeat: -1\n" + ok, "repeat"},
		{"bad name", "name: X/y\nwhy: y\n" + ok, "name"},
		{"name unlike the file", "name: other\nwhy: y\n" + ok, "file"},
		{"advance of nothing", "name: x\nwhy: y\nsteps:\n  - advance: {}\n  - judge: holds\n", "advance"},
		{"advance of both", "name: x\nwhy: y\nsteps:\n  - advance: {confirmations: 1m, scheduler: 1m}\n  - judge: holds\n", "advance"},
		{"reach without a tool", "name: x\nwhy: y\nsteps:\n  - reach: {nudge: go}\n  - judge: holds\n", "reach"},
		{"file of an unknown kind", "name: x\nwhy: y\nsteps:\n  - send: {kind: voice, name: a.ogg}\n  - judge: holds\n", "kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadScenario(writeScenario(t, t.TempDir(), "x", tc.body))
			if err == nil {
				t.Fatal("it loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestLoadScenariosPicksByName(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b", "a", "c"} {
		writeScenario(t, dir, n, "name: "+n+"\nwhy: y\nsteps:\n  - judge: holds\n")
	}
	all, err := LoadScenarios(dir, nil)
	if err != nil || len(all) != 3 || all[0].Name != "a" || all[2].Name != "c" {
		t.Fatalf("all %+v, %v", all, err)
	}
	some, err := LoadScenarios(dir, []string{"c", "a"})
	if err != nil || len(some) != 2 || some[0].Name != "c" {
		t.Fatalf("some %+v, %v", some, err)
	}
	if _, err := LoadScenarios(dir, []string{"nope"}); err == nil {
		t.Fatal("an unknown name loaded")
	}
	if _, err := LoadScenarios(dir, []string{"../a"}); err == nil {
		t.Fatal("a name with a path loaded")
	}
}

func TestSchemaMatchesTheLoader(t *testing.T) {
	b, err := schemas.Get("chatlab-scenario.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pointer string
		v       any
	}{
		{"", Scenario{}},
		{"/$defs/scriptStep", ScriptStep{}},
		{"/$defs/page", Page{}},
		{"/$defs/step", Step{}},
		{"/$defs/file", File{}},
		{"/$defs/advance", Advance{}},
		{"/$defs/reach", Reach{}},
		{"/$defs/journalFact", JournalFact{}},
		{"/$defs/text", Text{}},
		{"/$defs/jobFact", JobFact{}},
		{"/$defs/facts", Facts{}},
	} {
		o, err := schemas.At(b, tc.pointer)
		if err != nil {
			t.Fatal(err)
		}
		if want := schemas.YAMLFields(tc.v); !slices.Equal(o.Properties, want) {
			t.Errorf("%q: schema properties %v, loader fields %v", tc.pointer, o.Properties, want)
		}
		if o.AdditionalProperties {
			t.Errorf("%q: the loader rejects unknown fields; the schema must too", tc.pointer)
		}
	}
}

func TestShippedScenariosLoad(t *testing.T) {
	all, err := LoadScenarios("../../tools/chatlab/scenarios", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no scenarios")
	}
}
