// Package transcript renders a provider-neutral history as plain text
// lines ("User: …", "Assistant: …"), for models that take the
// conversation as text: compaction, and the claude CLI's stdin.
package transcript

import (
	"fmt"

	"github.com/igorrius/ganglion/provider"
)

// ToolCalls says whether the model's tool calls appear in the transcript.
type ToolCalls bool

// Tool call modes.
const (
	WithToolCalls    ToolCalls = true
	WithoutToolCalls ToolCalls = false
)

// Lines returns one line per message, oldest first. System messages are
// left out: each caller places them itself. An assistant message without
// text adds no "Assistant:" line.
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
			out = append(out, "Tool result: "+m.Content)
		case provider.RoleSystem:
		}
	}
	return out
}
