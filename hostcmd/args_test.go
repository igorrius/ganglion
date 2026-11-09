package hostcmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseExec(t *testing.T) {
	e, err := ParseExec(json.RawMessage(`{"argv":["df","-h"],"dir":"/tmp","timeout":30,"reason":" free\n space "}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Command.Dir != "/tmp" || len(e.Command.Argv) != 2 || e.Timeout != 30 || e.Reason != "free space" {
		t.Errorf("got %+v", e)
	}
	for name, raw := range map[string]string{
		"unknown field": `{"argv":["ls"],"shell":true}`,
		"empty argv":    `{"argv":[]}`,
		"empty program": `{"argv":[""]}`,
		"relative dir":  `{"argv":["ls"],"dir":"x"}`,
		"dotdot dir":    `{"argv":["ls"],"dir":"/a/../b"}`,
		"nul":           `{"argv":["ls","a\u0000b"]}`,
		"timeout":       `{"argv":["ls"],"timeout":901}`,
		"negative":      `{"argv":["ls"],"timeout":-1}`,
		"too many":      `{"argv":["` + strings.Repeat(`a","`, 64) + `a"]}`,
		"not json":      `[`,
	} {
		if _, err := ParseExec(json.RawMessage(raw)); !errors.Is(err, ErrArgs) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParsePlan(t *testing.T) {
	steps, err := ParsePlan(json.RawMessage(`{"steps":["look",{"text":"clean","argv":["go","clean","-cache"],"dir":"/tmp","reason":"space"},{"text":"note"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].Text != "look" || steps[0].Command != nil {
		t.Fatalf("steps %+v", steps)
	}
	if steps[1].Command == nil || steps[1].Command.Dir != "/tmp" || steps[1].Reason != "space" || steps[2].Command != nil {
		t.Errorf("steps %+v", steps)
	}
	for name, raw := range map[string]string{
		"unknown field": `{"steps":[{"text":"x","shell":"y"}]}`,
		"dir no argv":   `{"steps":[{"text":"x","dir":"/tmp"}]}`,
		"bad argv":      `{"steps":[{"text":"x","argv":[""]}]}`,
		"top unknown":   `{"steps":[],"x":1}`,
	} {
		if _, err := ParsePlan(json.RawMessage(raw)); !errors.Is(err, ErrArgs) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
