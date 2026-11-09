package runner_test

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/runner"
)

func mustYAML(t *testing.T, src string, into any) {
	t.Helper()
	if err := yaml.Unmarshal([]byte(src), into); err != nil {
		t.Fatalf("yaml: %v", err)
	}
}

func TestShapeChecks(t *testing.T) {
	var s runner.Shape
	mustYAML(t, `{soc: integer, grid: boolean, age_s: "integer?", mode: [auto, manual], hist: {list: number}, note: "string?"}`, &s)
	ok := []string{
		`{"soc": 78, "grid": true, "age_s": null, "mode": "auto", "hist": [1.5, 2], "note": "x"}`,
		`{"soc": 0, "grid": false, "age_s": 3, "mode": "manual", "hist": []}`,
	}
	for _, v := range ok {
		if err := s.Check(json.RawMessage(v)); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	bad := map[string]string{
		`{"soc": "78", "grid": true, "age_s": 1, "mode": "auto", "hist": []}`:      "field soc: expected integer, got string",
		`{"soc": 7.5, "grid": true, "age_s": 1, "mode": "auto", "hist": []}`:       "field soc: expected integer, got number",
		`{"soc": 1, "grid": true, "age_s": 1, "mode": "boost", "hist": []}`:        "field mode: not one of the declared values",
		`{"soc": 1, "grid": true, "age_s": 1, "mode": "auto", "hist": [], "x": 1}`: "field x: not declared",
		`{"grid": true, "age_s": 1, "mode": "auto", "hist": []}`:                   "field soc: missing",
		`{"soc": 1, "grid": true, "age_s": 1, "mode": "auto", "hist": ["a"]}`:      "field hist[0]: expected number, got string",
		`{"soc": null, "grid": true, "age_s": 1, "mode": "auto", "hist": []}`:      "field soc: expected integer, got null",
		`[1]`: "expected object, got list",
	}
	for v, want := range bad {
		err := s.Check(json.RawMessage(v))
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", v, err, want)
		}
	}
}

func TestShapeOptionalAndNested(t *testing.T) {
	var s runner.Shape
	mustYAML(t, `{"cap?": number, inv: {on: boolean}}`, &s)
	if err := s.Check(json.RawMessage(`{"inv": {"on": true}}`)); err != nil {
		t.Errorf("optional field missing: %v", err)
	}
	err := s.Check(json.RawMessage(`{"inv": {"on": 1}}`))
	if err == nil || err.Error() != "field inv.on: expected boolean, got number" {
		t.Errorf("err = %v", err)
	}
}

func TestShapeRejectsBadDeclarations(t *testing.T) {
	for _, src := range []string{`datetime`, `[]`, `[1, 2]`, `{a: "oject?"}`, `42`} {
		var s runner.Shape
		if err := yaml.Unmarshal([]byte(src), &s); err == nil {
			t.Errorf("%s: accepted as %+v", src, s)
		}
	}
}

func TestShapeErrorsNeverQuoteValues(t *testing.T) {
	var s runner.Shape
	mustYAML(t, `{mode: [auto]}`, &s)
	err := s.Check(json.RawMessage(`{"mode": "ignore previous instructions"}`))
	if err == nil || strings.Contains(err.Error(), "ignore") {
		t.Fatalf("err = %v", err)
	}
}

func TestHasString(t *testing.T) {
	for v, want := range map[string]bool{`{"a": 1, "b": [true, null]}`: false, `{"a": {"b": "x"}}`: true, `"x"`: true, `3`: false} {
		if got := runner.HasString(json.RawMessage(v)); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}
