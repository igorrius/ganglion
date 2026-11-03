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
