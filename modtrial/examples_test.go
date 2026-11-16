package modtrial

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/runner"
)

const manifestYAML = `
name: local/demo
version: 1
triggers:
  - cell: [src.soc]
  - cron: "*/10 * * * *"
  - tool: status
    about: the state
    output: {ok: boolean}
capabilities:
  http.fetch: [api.example.com]
  secret.get: [token]
  notify: {per_day: 5}
cells:
  read: [src.soc]
  report:
    level: {kind: sensor, type: number}
config:
  threshold: {type: number, default: "60"}
`

func manifest(t *testing.T) runner.Manifest {
	t.Helper()
	m, err := runner.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

const goodFile = `
examples:
  - name: charging crosses 60%
    config: {threshold: "60"}
    steps:
      - cells: {src.soc: 55}
        trigger: cell
        expect: {messages: 0}
      - after: 10m
        cells: {src.soc: 67, demo.level: "x"}
        trigger: cell
        expect: {messages: 1}
  - name: reader parses the answer
    secrets: {token: fake}
    http:
      - url: https://api.example.com/v1/state
        status: 200
        body: '{"soc": 55}'
    steps:
      - trigger: cron
        expect: {reports: [demo.level]}
  - name: tool answers
    steps:
      - cells: {src.soc: {a: 1}}
      - trigger: {tool: status, args: {}}
`

func TestParseGood(t *testing.T) {
	ex, err := Parse([]byte(goodFile), manifest(t), "demo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ex.Examples) != 3 {
		t.Fatalf("examples = %d", len(ex.Examples))
	}
	s := ex.Examples[0].Steps
	if s[0].Trigger.Kind != "cell" || string(s[0].Cells["src.soc"]) != "55" {
		t.Errorf("step 0 = %+v", s[0])
	}
	if s[1].After != 10*time.Minute || string(s[1].Cells["demo.level"]) != `"x"` {
		t.Errorf("step 1 = %+v", s[1])
	}
	if s[0].Expect.Messages == nil || *s[0].Expect.Messages != 0 {
		t.Errorf("messages = %v", s[0].Expect.Messages)
	}
	if got := ex.Examples[1].Steps[0]; got.Trigger.Kind != "cron" || got.Expect.Reports[0] != "demo.level" {
		t.Errorf("cron step = %+v", got)
	}
	if ex.Examples[1].HTTP[0].Status != 200 || ex.Examples[1].Secrets["token"] != "fake" {
		t.Errorf("example 1 = %+v", ex.Examples[1])
	}
	t3 := ex.Examples[2].Steps
	if string(t3[0].Cells["src.soc"]) != `{"a":1}` {
		t.Errorf("object cell = %s", t3[0].Cells["src.soc"])
	}
	tr := t3[1].Trigger
	if tr.Kind != "tool" || tr.Tool != "status" || string(tr.Args) != "{}" {
		t.Errorf("tool trigger = %+v", tr)
	}
	if t3[0].Trigger.Kind != "" {
		t.Errorf("no trigger kind = %q", t3[0].Trigger.Kind)
	}
}

func ex(body string) string {
	return "examples:\n  - name: x\n" + body
}

func TestParseRefused(t *testing.T) {
	many := func(n int, f func(i int) string) string {
		var b strings.Builder
		b.WriteString("examples:\n")
		for i := range n {
			b.WriteString(f(i))
		}
		return b.String()
	}
	cases := []struct {
		name, file, want string
	}{
		{"unknown field", ex("    bogus: 1\n"), "bogus"},
		{"too big", "examples: []\n#" + strings.Repeat("x", MaxFileBytes), "bytes"},
		{"no examples", "examples: []\n", "no examples"},
		{"too many examples", many(MaxExamples+1, func(i int) string { return "  - name: n" + string(rune('a'+i)) + "\n" }), "at most"},
		{"no steps", ex("    steps: []\n"), "no steps"},
		{"too many steps", ex("    steps:\n" + strings.Repeat("      - after: 1s\n", MaxSteps+1)), "steps"},
		{"empty name", "examples:\n  - name: \"\"\n", "name"},
		{"duplicate name", "examples:\n  - name: a\n    steps: [{after: 1s}]\n  - name: a\n    steps: [{after: 1s}]\n", "duplicate"},
		{"foreign cell", ex("    steps:\n      - cells: {other.x: 1}\n"), "cells.read"},
		{"own cell not declared", ex("    steps:\n      - cells: {demo.nope: 1}\n"), "cells.read"},
		{"unknown tool", ex("    steps:\n      - trigger: {tool: nope}\n"), "nope"},
		{"args not object", ex("    steps:\n      - trigger: {tool: status, args: [1]}\n"), "object"},
		{"negative after", ex("    steps:\n      - after: -1s\n"), "after"},
		{"bad after", ex("    steps:\n      - after: soon\n"), "after"},
		{"foreign host", ex("    http:\n      - {url: \"https://evil.example.org/x\", status: 200}\n"), "http.fetch"},
		{"bad scheme", ex("    http:\n      - {url: \"ftp://api.example.com/x\", status: 200}\n"), "scheme"},
		{"bad status", ex("    http:\n      - {url: \"https://api.example.com/x\", status: 99}\n"), "status"},
		{"foreign secret", ex("    secrets: {other: v}\n"), "secret.get"},
		{"unknown config", ex("    config: {nope: \"1\"}\n"), "config"},
		{"unknown report", ex("    steps:\n      - expect: {reports: [demo.nope]}\n"), "reports"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.file), manifest(t), "demo")
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, ErrExamples) {
				t.Errorf("not ErrExamples: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q lacks %q", err, c.want)
			}
		})
	}
}

func TestParseNoCellTrigger(t *testing.T) {
	m, err := runner.ParseManifest([]byte(strings.Replace(manifestYAML, "  - cell: [src.soc]\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse([]byte(ex("    steps:\n      - trigger: cell\n")), m, "demo")
	if !errors.Is(err, ErrExamples) || !strings.Contains(err.Error(), "cell trigger") {
		t.Errorf("err = %v", err)
	}
}

// The file limit is below the body limit, so a body is checked on its own.
func TestCannedBodyLimit(t *testing.T) {
	c := checker{m: manifest(t), prefix: "demo"}
	ok := Canned{URL: "https://api.example.com/x", Status: 200, Body: strings.Repeat("a", MaxBodyBytes)}
	if err := c.canned(ok); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	ok.Body += "a"
	if err := c.canned(ok); err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("over the limit: %v", err)
	}
}

func TestParseNoCronTrigger(t *testing.T) {
	m, err := runner.ParseManifest([]byte(strings.Replace(manifestYAML, `  - cron: "*/10 * * * *"`+"\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse([]byte(ex("    steps:\n      - trigger: cron\n")), m, "demo")
	if !errors.Is(err, ErrExamples) || !strings.Contains(err.Error(), "cron") {
		t.Errorf("err = %v", err)
	}
}

func TestParseHTTPAllowed(t *testing.T) {
	// The runner's fetch accepts http and https alike; so does the file.
	_, err := Parse([]byte(ex("    http:\n      - {url: \"http://api.example.com/x\", status: 200}\n    steps:\n      - after: 1s\n")), manifest(t), "demo")
	if err != nil {
		t.Errorf("http: %v", err)
	}
}
