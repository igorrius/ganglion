package chatlab

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/tool/journal"
)

func TestCheck(t *testing.T) {
	view := View{
		Journal: []journal.Entry{
			{Tool: "memory.write", Decision: journal.DecisionConfirm, ResultHead: "not done yet: awaiting"},
			{Tool: "web.fetch", Decision: journal.DecisionAllowed, ResultHead: "page text"},
			{Tool: "web.fetch", Decision: journal.DecisionAllowed, ResultHead: "other page"},
		},
		Messages: []Message{
			{Text: "Create?\n\nExpired.", Replaced: true},
			{Text: "Create again?", Buttons: []string{"Approve", "Deny"}},
			{Text: "the picture", Files: []Attachment{{Kind: KindPhoto}}},
			{Text: "Details", Buttons: []string{"Delete"}, Replaced: true},
		},
		Last:    []Message{{Text: "It waits for your answer."}, {Text: "Second line."}},
		History: "user: hi\ntool: [reminder] sent",
		Memory:  map[string]string{"memory/boiler.md": "The boiler holds 80 litres."},
		Jobs:    map[string]int{"reminder": 1},
	}
	for _, tc := range []struct {
		name string
		f    Facts
		want string // "" holds; otherwise what the failure names
	}{
		{"a journal row", Facts{Journal: []JournalFact{{Tool: "memory.write", Decision: "confirm"}}}, ""},
		{"no such tool", Facts{Journal: []JournalFact{{Tool: "memory.forget"}}}, "memory.forget"},
		{"another decision", Facts{Journal: []JournalFact{{Tool: "memory.write", Decision: "approved"}}}, "approved"},
		{"a result", Facts{Journal: []JournalFact{{Tool: "memory.write", ResultContains: "not done yet"}}}, ""},
		{"another result", Facts{Journal: []JournalFact{{Tool: "memory.write", ResultContains: "stored"}}}, "stored"},
		{"a count", Facts{Journal: []JournalFact{{Tool: "web.fetch", Count: new(2)}}}, ""},
		{"a wrong count", Facts{Journal: []JournalFact{{Tool: "web.fetch", Count: new(1)}}}, "found 2"},
		{"a count of none", Facts{Journal: []JournalFact{{Tool: "memory.forget", Count: new(0)}}}, ""},
		{"no row wanted", Facts{NoJournal: []JournalFact{{Tool: "memory.write", Decision: "approved"}}}, ""},
		{"a row not wanted", Facts{NoJournal: []JournalFact{{Tool: "memory.write", Decision: "confirm"}}}, "memory.write"},
		{"questions", Facts{Questions: new(1)}, ""},
		{"wrong questions", Facts{Questions: new(2)}, "questions"},
		{"replaced", Facts{MessageReplaced: "Expired."}, ""},
		{"not replaced", Facts{MessageReplaced: "Create again?"}, "Create again?"},
		{"replaced but with buttons", Facts{MessageReplaced: "Details"}, "Details"},
		{"a reply", Facts{Reply: &Text{Contains: "waits"}}, ""},
		{"a reply lacking", Facts{Reply: &Text{Contains: "saved"}}, "saved"},
		{"a reply reading too much", Facts{Reply: &Text{NotContains: "Second"}}, "Second"},
		{"history", Facts{History: &Text{Contains: "[reminder]"}}, ""},
		{"history lacking", Facts{History: &Text{Contains: "not done yet"}}, "not done yet"},
		{"memory", Facts{Memory: &Text{Contains: "80 litres"}}, ""},
		{"memory holding", Facts{Memory: &Text{NotContains: "80 litres"}}, "80 litres"},
		{"files", Facts{Files: new(1)}, ""},
		{"wrong files", Facts{Files: new(2)}, "files"},
		{"jobs", Facts{Jobs: &JobFact{Kind: "reminder", Count: 1}}, ""},
		{"no jobs of a kind", Facts{Jobs: &JobFact{Kind: "task", Count: 0}}, ""},
		{"wrong jobs", Facts{Jobs: &JobFact{Kind: "reminder", Count: 0}}, "reminder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.f, view)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("failed: %v", got)
			case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
				t.Fatalf("got %v, want one line naming %q", got, tc.want)
			}
		})
	}
}

func TestCheckQuestionsCountsOnlyWhatShows(t *testing.T) {
	v := View{Messages: []Message{{Text: "Create?\n\nExpired.", Replaced: true}, {Text: "Run?", Buttons: []string{"Details"}}}}
	if got := Check(Facts{Questions: new(0)}, v); len(got) != 0 {
		t.Fatalf("a closed question counted: %v", got)
	}
}

func TestCheckReportsEveryFailure(t *testing.T) {
	got := Check(Facts{Questions: new(9), Files: new(9)}, View{})
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestTakenAndReplies(t *testing.T) {
	one, two := 1, 2
	v := View{Taken: 1, Last: []Message{{Text: "a"}, {Text: "b"}}}
	if got := Check(Facts{Taken: &one, Replies: &two}, v); len(got) != 0 {
		t.Fatalf("failures %v", got)
	}
	if got := Check(Facts{Taken: &two, Replies: &one}, v); len(got) != 2 {
		t.Fatalf("failures %v", got)
	}
}
