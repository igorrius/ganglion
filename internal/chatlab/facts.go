package chatlab

import (
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/tool/journal"
)

// approveLabel marks a question: a message waiting for the owner's answer.
const approveLabel = "Approve"

// View is what a facts step can read of a node.
type View struct {
	Journal  []journal.Entry
	Messages []Message
	Last     []Message // the model's words since the last action
	History  string
	Memory   map[string]string // stored files by path
	Jobs     map[string]int    // live jobs by kind, without system jobs
	Taken    int               // messages that joined a running turn
	Tainted  bool              // the chat's session holds outside content
}

// Check returns one line per failed check, saying what was wanted and what
// was found; none means the facts hold.
func Check(f Facts, v View) []string {
	var out []string
	for _, jf := range f.Journal {
		out = append(out, checkJournal(jf, v.Journal)...)
	}
	for _, jf := range f.NoJournal {
		if n := len(matching(jf, v.Journal)); n > 0 {
			out = append(out, fmt.Sprintf("journal: wanted no %s, found %d", describe(jf), n))
		}
	}
	if n := questions(v.Messages); f.Questions != nil && n != *f.Questions {
		out = append(out, fmt.Sprintf("questions: wanted %d showing, found %d", *f.Questions, n))
	}
	if f.MessageReplaced != "" && !slices.ContainsFunc(v.Messages, func(m Message) bool {
		return m.Replaced && len(m.Buttons) == 0 && strings.Contains(m.Text, f.MessageReplaced)
	}) {
		out = append(out, fmt.Sprintf("message_replaced: no edited message without buttons holds %q", f.MessageReplaced))
	}
	out = append(out, checkText("question", f.Question, words(openQuestions(v.Messages)))...)
	out = append(out, checkText("reply", f.Reply, words(v.Last))...)
	out = append(out, checkText("history", f.History, v.History)...)
	memory := make([]string, 0, len(v.Memory))
	for _, body := range v.Memory {
		memory = append(memory, body)
	}
	out = append(out, checkText("memory", f.Memory, strings.Join(memory, "\n"))...)
	if n := filesShown(v.Messages); f.Files != nil && n != *f.Files {
		out = append(out, fmt.Sprintf("files: wanted %d shown, found %d", *f.Files, n))
	}
	if f.Taken != nil && v.Taken != *f.Taken {
		out = append(out, fmt.Sprintf("taken: wanted %d messages joining a turn, found %d", *f.Taken, v.Taken))
	}
	if f.Replies != nil && len(v.Last) != *f.Replies {
		out = append(out, fmt.Sprintf("replies: wanted %d from the model, found %d", *f.Replies, len(v.Last)))
	}
	if f.Tainted != nil && v.Tainted != *f.Tainted {
		out = append(out, fmt.Sprintf("tainted: wanted %t, found %t", *f.Tainted, v.Tainted))
	}
	if f.Jobs != nil && v.Jobs[f.Jobs.Kind] != f.Jobs.Count {
		out = append(out, fmt.Sprintf("jobs: wanted %d of kind %s, found %d", f.Jobs.Count, f.Jobs.Kind, v.Jobs[f.Jobs.Kind]))
	}
	return out
}

func questions(ms []Message) int { return len(openQuestions(ms)) }

// openQuestions are the messages showing an Approve button now.
func openQuestions(ms []Message) []Message {
	var out []Message
	for _, m := range ms {
		if slices.Contains(m.Buttons, approveLabel) {
			out = append(out, m)
		}
	}
	return out
}

func filesShown(ms []Message) int {
	n := 0
	for _, m := range ms {
		n += len(m.Files)
	}
	return n
}

func checkJournal(jf JournalFact, entries []journal.Entry) []string {
	n := len(matching(jf, entries))
	switch {
	case jf.Count == nil && n == 0:
		return []string{"journal: wanted " + describe(jf) + ", found none"}
	case jf.Count != nil && n != *jf.Count:
		return []string{fmt.Sprintf("journal: wanted %d of %s, found %d", *jf.Count, describe(jf), n)}
	}
	return nil
}

func matching(jf JournalFact, entries []journal.Entry) []journal.Entry {
	var out []journal.Entry
	for _, e := range entries {
		if e.Tool == jf.Tool && (jf.Decision == "" || string(e.Decision) == jf.Decision) &&
			strings.Contains(e.ResultHead, jf.ResultContains) {
			out = append(out, e)
		}
	}
	return out
}

func describe(jf JournalFact) string {
	s := jf.Tool
	if jf.Decision != "" {
		s += " " + jf.Decision
	}
	if jf.ResultContains != "" {
		s += fmt.Sprintf(" with %q", jf.ResultContains)
	}
	return s
}

func checkText(field string, t *Text, text string) []string {
	if t == nil {
		return nil
	}
	var out []string
	if t.Contains != "" && !strings.Contains(text, t.Contains) {
		out = append(out, fmt.Sprintf("%s: does not hold %q", field, t.Contains))
	}
	if t.NotContains != "" && strings.Contains(text, t.NotContains) {
		out = append(out, fmt.Sprintf("%s: holds %q", field, t.NotContains))
	}
	return out
}

// words is the text of messages, one after another.
func words(ms []Message) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Text
	}
	return strings.Join(parts, "\n")
}
