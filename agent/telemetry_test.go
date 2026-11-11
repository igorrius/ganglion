package agent

import (
	"testing"

	"github.com/igorrius/ganglion/turns"
)

// The turn's log line and /status name the model that answered: the first
// attempt that did not fail, not a compaction's or a check's after it. On
// Railway a turn Sonnet answered was logged as Haiku's (2026-11-11).
func TestAnsweredByIsTheFirstAttemptThatDidNotFail(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route []turns.Attempt
		want  string
	}{
		{"none", nil, ""},
		{"all failed", []turns.Attempt{{Model: "sonnet", Error: "timeout"}}, ""},
		{"answer, then a compaction", []turns.Attempt{{Model: "sonnet"}, {Model: "haiku"}}, "sonnet"},
		{"a fallback", []turns.Attempt{{Model: "sonnet", Error: "limit"}, {Model: "haiku"}}, "haiku"},
	} {
		if at, ok := answeredBy(tc.route); at.Model != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: answered by %q (%v), want %q", tc.name, at.Model, ok, tc.want)
		}
	}
}
