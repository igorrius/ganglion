package agent

import (
	"encoding/json"
	"testing"

	"github.com/igorrius/ganglion/provider"
)

// The text the compaction model gets, pinned byte for byte.
func TestCompactInput(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "Igor: hi"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "clock", Args: json.RawMessage(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "1", Content: "12:00"},
		{Role: provider.RoleAssistant, Content: "Noon."},
	}
	if got, want := compactInput("", msgs), "User: Igor: hi\nTool result: 12:00\nAssistant: Noon.\n"; got != want {
		t.Errorf("first compaction\n%q\nwant\n%q", got, want)
	}
	want := "Earlier summary:\nold\n\nNew part of the conversation:\nUser: Igor: hi\nTool result: 12:00\nAssistant: Noon.\n"
	if got := compactInput("old", msgs); got != want {
		t.Errorf("later compaction\n%q\nwant\n%q", got, want)
	}
}
