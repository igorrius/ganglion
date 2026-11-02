package claudecli

import (
	"encoding/json"
	"testing"

	"github.com/igorrius/ganglion/provider"
)

// The text the CLI gets on stdin, pinned byte for byte.
func TestPromptSplitsSystemAndRendersTheTranscript(t *testing.T) {
	system, stdin := prompt([]provider.Message{
		{Role: provider.RoleSystem, Content: "rules"},
		{Role: provider.RoleSystem, Content: "Summary: earlier"},
		{Role: provider.RoleUser, Content: "Igor: hi"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "clock", Args: json.RawMessage(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "1", Content: "12:00"},
		{Role: provider.RoleAssistant, Content: "Noon."},
	})
	if system != "rules" {
		t.Errorf("system %q", system)
	}
	want := "Summary: earlier\n\nConversation so far, oldest first:\n\n" +
		"User: Igor: hi\n\nAssistant called tool clock with {}\n\nTool result: 12:00\n\nAssistant: Noon."
	if stdin != want {
		t.Errorf("stdin\n%q\nwant\n%q", stdin, want)
	}
}

func TestEmptyCommandKeepsTheDefault(t *testing.T) {
	if c := New("cli", WithCommand("")); c.command != "claude" {
		t.Fatalf("command %q", c.command)
	}
}
