package transcript

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/provider"
)

var history = []provider.Message{
	{Role: provider.RoleSystem, Content: "rules"},
	{Role: provider.RoleUser, Content: "Igor: hi"},
	{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "clock", Args: json.RawMessage(`{}`)}}},
	{Role: provider.RoleTool, ToolCallID: "1", Content: "12:00"},
	{Role: provider.RoleAssistant, Content: "It is noon."},
}

func TestLinesWithToolCalls(t *testing.T) {
	want := []string{"User: Igor: hi", "Assistant called tool clock with {}", "Tool result: 12:00", "Assistant: It is noon."}
	if got := Lines(history, WithToolCalls); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLinesWithoutToolCalls(t *testing.T) {
	want := []string{"User: Igor: hi", "Tool result: 12:00", "Assistant: It is noon."}
	if got := Lines(history, WithoutToolCalls); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
