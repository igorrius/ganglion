package memory_test

import (
	"errors"
	"testing"

	"github.com/igorrius/ganglion/memory"
)

func TestParseOps(t *testing.T) {
	good := map[string][]memory.Op{
		`{"ops":[]}`:                 nil,
		"```json\n{\"ops\":[]}\n```": nil,
		`{"ops":[{"kind":"add","topic":"Inverter","summary":"The Acme inverter","text":"Negative batt_w is charging."},` +
			`{"kind":"replace","topic":"car","fact":"f2","text":"The car is red."},{"kind":"remove","topic":"car","fact":"f1"}]}`: {
			{Kind: memory.OpAdd, Topic: "Inverter", Summary: "The Acme inverter", Text: "Negative batt_w is charging."},
			{Kind: memory.OpReplace, Topic: "car", Fact: "f2", Text: "The car is red."},
			{Kind: memory.OpRemove, Topic: "car", Fact: "f1"},
		},
	}
	for in, want := range good {
		got, err := memory.ParseOps(in)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%q: %+v %v", in, got, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: op %d %+v, want %+v", in, i, got[i], want[i])
			}
		}
	}
	six := `{"ops":[` + `{"kind":"add","topic":"a","summary":"s","text":"x"},` + `{"kind":"add","topic":"a","text":"x"},` +
		`{"kind":"add","topic":"a","text":"x"},{"kind":"add","topic":"a","text":"x"},{"kind":"add","topic":"a","text":"x"},` +
		`{"kind":"add","topic":"a","text":"x"}]}`
	for _, in := range []string{
		"I found nothing to store.",
		`{"ops":[]} {"ops":[]}`,
		`{"ops":[{"kind":"drop","topic":"car"}]}`,
		`{"ops":[{"kind":"rename","topic":"car"}]}`,
		`{"ops":[{"kind":"add","topic":"car","text":"x","why":"because"}]}`,
		`{"ops":[], "note":"x"}`,
		`{"ops":{}}`,
		`{"ops":[{"kind":"remove","topic":"car","fact":"f1](http://evil.example)"}]}`,
		`{"ops":[{"kind":"add","topic":"car","text":"x <!-- 2026-01-01 -->"}]}`,
		`{}`,
		six,
	} {
		if _, err := memory.ParseOps(in); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("%q: want ErrInvalid, got %v", in, err)
		}
	}
}
