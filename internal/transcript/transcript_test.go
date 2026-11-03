package transcript

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
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

// A long tool result is cut with a note; one marked as outside content
// keeps its closing tag (ruling R30); the opening tag counts as text.
func TestLongToolResultIsBounded(t *testing.T) {
	page := tool.WrapExternal("web.fetch", strings.Repeat("я", MaxToolResult+10))
	msgs := []provider.Message{{Role: provider.RoleTool, ToolCallID: "1", Content: page}, {Role: provider.RoleTool, ToolCallID: "2", Content: strings.Repeat("x", MaxToolResult+3)}}
	got := Lines(msgs, WithToolCalls)
	if !strings.HasSuffix(got[0], "[40 more characters left out]"+tool.ExternalClose) || !strings.HasPrefix(got[0], `Tool result: <external source="web.fetch">`) {
		t.Fatalf("wrapped result %q", got[0][len(got[0])-80:])
	}
	if !strings.HasSuffix(got[1], strings.Repeat("x", 10)+"\n[3 more characters left out]") {
		t.Fatalf("plain result ends %q", got[1][len(got[1])-40:])
	}
	if short := Lines(history, WithToolCalls); short[2] != "Tool result: 12:00" {
		t.Fatalf("short result changed: %q", short[2])
	}
}
