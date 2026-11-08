package builtin

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/tool"
)

// Errors of an action a confirmation question cannot show whole (ruling
// R34). The policy cuts a question at tool.MaxQuestionRunes and removes
// secrets from it, so the owner would approve something other than what
// runs; such a call is refused with a fixed error, without a question.
var (
	ErrTooLongToShow = errors.New("too long to show the owner whole in a confirmation question; make it shorter or split it")
	ErrSecretShown   = errors.New("holds something that looks like a secret, which the question would hide from the owner; leave it out")
)

// maxNameRunes bounds a recipient's name in a question.
const maxNameRunes = 64

// showable checks a text the question shows: at most limit runes, and
// nothing redact.String would change. The text is checked as a question
// shows it, after words, so a text that is a JSON document alone is not
// read as one.
func showable(name, s string, limit int) error {
	if utf8.RuneCountInString(s) > limit {
		return fmt.Errorf("%s: %w", name, ErrTooLongToShow)
	}
	if shown := "Text: " + s; redact.String(shown) != shown {
		return fmt.Errorf("%s: %w", name, ErrSecretShown)
	}
	return nil
}

// whole returns question q if the owner sees it as it is: no longer than
// tool.MaxQuestionRunes and with nothing for the policy to redact.
func whole(name, q string) (string, error) {
	if err := showable(name, q, tool.MaxQuestionRunes); err != nil {
		return "", err
	}
	return q, nil
}

// oneLine is s on one line, at most maxNameRunes: a name cannot add lines
// to a question.
func oneLine(s string) string {
	return text.Truncate(strings.Join(strings.Fields(s), " "), maxNameRunes)
}

// codeSpan is s as one Markdown code span that shows s exactly: fenced by
// more backticks than the longest run in s, so a backtick inside cannot end
// the span early and let what follows render as Markdown (a link would hide
// its target). The spaces inside a longer fence are the ones Markdown drops.
func codeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	if longest == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}
