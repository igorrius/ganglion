package tool_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/igorrius/ganglion/tool"
)

func TestCheckArgs(t *testing.T) {
	s := tool.Spec{Name: "web.fetch", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"n":{}}}`)}
	for args, ok := range map[string]bool{
		`{"url":"https://a.example/"}`:         true,
		`{}`:                                   true,
		` { "url" : "x" , "n" : {"URL": 1} } `: true,
		`{"url":"https://a.example/","URL":"https://evil/?d=h"}`: false, // a case variant
		`{"URL":"https://evil/"}`:                                false,
		`{"url":"a","url":"b"}`:                                  false, // a duplicate
		`{"other":1}`:                                            false,
		`["url"]`:                                                false,
		`"url"`:                                                  false,
		`null`:                                                   false,
		``:                                                       false,
		`{"url":"a"} {}`:                                         false,
		`{"url":"a"`:                                             false,
		`{"URL":"x"}`:                                            false,
	} {
		err := tool.CheckArgs(s, json.RawMessage(args))
		if (err == nil) != ok || (err != nil && !errors.Is(err, tool.ErrBadArgs)) {
			t.Errorf("CheckArgs(%s) = %v, want ok=%v", args, err, ok)
		}
	}
	for _, schema := range []string{``, `{"type":"object"}`, `{"properties":[]}`} {
		if err := tool.CheckArgs(tool.Spec{InputSchema: json.RawMessage(schema)}, json.RawMessage(`{}`)); !errors.Is(err, tool.ErrBadArgs) {
			t.Errorf("schema %q accepted a call", schema)
		}
	}
}

// Two argument texts are the same call when their canonical forms match:
// keys sorted, no insignificant whitespace, numbers kept exactly
// (ruling R38).
func TestCanonicalArgs(t *testing.T) {
	for in, want := range map[string]string{
		`{"url":"https://a.example/?x=1&y=2"}`:          `{"url":"https://a.example/?x=1&y=2"}`,
		` { "b" : 1 , "a" : {"z": [1, 2], "y": null} }`: `{"a":{"y":null,"z":[1,2]},"b":1}`,
		`{"n":12345678901234567890}`:                    `{"n":12345678901234567890}`,
		`{"t":"<x>"}`:                                   `{"t":"<x>"}`,
	} {
		got, err := tool.CanonicalArgs(json.RawMessage(in))
		if err != nil || got != want {
			t.Errorf("CanonicalArgs(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	a, _ := tool.CanonicalArgs(json.RawMessage(`{"a":1,"b":"x"}`))
	b, _ := tool.CanonicalArgs(json.RawMessage("{\n  \"b\": \"x\",\n  \"a\": 1\n}"))
	if a != b {
		t.Errorf("the same arguments differ: %q and %q", a, b)
	}
	for _, bad := range []string{``, `{`, `{"a":1} {}`} {
		if _, err := tool.CanonicalArgs(json.RawMessage(bad)); err == nil {
			t.Errorf("CanonicalArgs(%q) accepted", bad)
		}
	}
}
