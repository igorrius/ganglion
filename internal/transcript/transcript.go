// Package transcript renders a provider-neutral history as plain text
// lines ("User: …", "Assistant: …"), for models that take the
// conversation as text: compaction, and the claude CLI's stdin.
package transcript

import (
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
)

// MaxToolResult is how much of one tool result a transcript keeps, in
// runes: a page a tool read earlier stays readable without filling the
// prompt. The API path sends results whole; compaction bounds both.
const MaxToolResult = 4000

// ToolCalls says whether the model's tool calls appear in the transcript.
type ToolCalls bool

// Tool call modes.
const (
	WithToolCalls    ToolCalls = true
	WithoutToolCalls ToolCalls = false
)

// Lines returns one line per message, oldest first. System messages are
// left out: each caller places them itself. An assistant message without
// text adds no "Assistant:" line. Every message's own lines after its
// first are indented, in every role, so neither outside content nor a
// family member's message can fake a turn (Indent).
func Lines(msgs []provider.Message, calls ToolCalls) []string {
	var out []string
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			out = append(out, "User: "+Indent(m.Content))
		case provider.RoleAssistant:
			if m.Content != "" {
				out = append(out, "Assistant: "+Indent(m.Content))
			}
			if calls {
				for _, tc := range m.ToolCalls {
					out = append(out, Indent(fmt.Sprintf("Assistant called tool %s with %s", tc.Name, tc.Args)))
				}
			}
		case provider.RoleTool:
			out = append(out, "Tool result: "+Indent(Cap(m.Content)))
		case provider.RoleSystem:
		}
	}
	return out
}

// Cap is a tool result cut to MaxToolResult runes with a note of how
// much was left out. A result marked as outside content keeps its
// closing tag, so what follows is not read as part of it. The agent caps
// a confirmed call's outcome with it too, since that note reaches the
// model as a user message.
func Cap(s string) string {
	body, closing := s, ""
	if inner, ok := strings.CutSuffix(s, tool.ExternalClose); ok {
		body, closing = inner, tool.ExternalClose
	}
	r := []rune(body)
	if len(r) <= MaxToolResult {
		return s
	}
	return string(r[:MaxToolResult]) + fmt.Sprintf("\n[%d more characters left out]", len(r)-MaxToolResult) + closing
}

// lineIndent starts every line of a message after its first, so no line
// inside a message can start like a turn ("User: …") in the transcript.
const lineIndent = "  "

// lineBreaks turns every sequence a reader may take as a line break into
// a line feed followed by lineIndent: CR LF, CR, LF, NEL (U+0085), the
// line and paragraph separators (U+2028, U+2029), and the vertical tab
// and form feed. CR LF is listed first so it counts once.
var lineBreaks = strings.NewReplacer(
	"\r\n", "\n"+lineIndent,
	"\r", "\n"+lineIndent,
	"\n", "\n"+lineIndent,
	"\u0085", "\n"+lineIndent,
	"\u2028", "\n"+lineIndent,
	"\u2029", "\n"+lineIndent,
	"\v", "\n"+lineIndent,
	"\f", "\n"+lineIndent,
)

// Indent puts lineIndent before every line of s after the first, whatever
// line break starts it.
func Indent(s string) string {
	return lineBreaks.Replace(s)
}
