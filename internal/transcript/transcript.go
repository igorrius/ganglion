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
// text adds no "Assistant:" line. A tool result's own lines are indented,
// so outside content cannot fake a turn.
func Lines(msgs []provider.Message, calls ToolCalls) []string {
	var out []string
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			out = append(out, "User: "+m.Content)
		case provider.RoleAssistant:
			if m.Content != "" {
				out = append(out, "Assistant: "+m.Content)
			}
			if calls {
				for _, tc := range m.ToolCalls {
					out = append(out, fmt.Sprintf("Assistant called tool %s with %s", tc.Name, tc.Args))
				}
			}
		case provider.RoleTool:
			out = append(out, "Tool result: "+indent(toolResult(m.Content)))
		case provider.RoleSystem:
		}
	}
	return out
}

// toolResult is a tool result cut to MaxToolResult runes with a note of
// how much was left out. A result marked as outside content keeps its
// closing tag, so what follows is not read as part of it.
func toolResult(s string) string {
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

// resultIndent starts every line of a tool result after its first, so no
// line of outside content can start like a turn ("User: …") in the
// transcript.
const resultIndent = "  "

// indent puts resultIndent before every line of s after the first.
func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n"+resultIndent)
}
