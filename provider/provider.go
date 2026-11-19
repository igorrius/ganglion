// Package provider defines model backends: an OpenAI-compatible client
// first (which also covers Ollama), the Anthropic Messages API later, and
// the claude CLI worker (docs/spec.md, section 7.4).
//
// The types here are the neutral history format of ADR-0020: each client
// translates them to and from its wire format. Capabilities and prices are
// per model and live in the router's catalogue.
package provider

import (
	"context"
	"encoding/json"

	"github.com/igorrius/ganglion/router"
)

// Role is who wrote a message of the history. Roles are stored in
// conversation state, so their values never change.
type Role string

// Roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of the provider-neutral history the core keeps
// (R-AGT-2, ADR-0020).
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool only: the call this result answers
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Tool describes a tool in MCP's shape; clients translate it (R-PRV-3).
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Usage is what a call cost, recorded in the turns table. The cache
// counts are part of InputTokens.
type Usage struct {
	InputTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
	OutputTokens     int
}

// Exec runs one tool call for a provider that runs its own tool loop
// (CLI providers, R-PRV-6). API providers ignore it and return
// tool_calls. The text is what the model reads; an error is a failed
// call, which the model reads too.
type Exec func(ctx context.Context, call ToolCall) (string, error)

// ToolsFunc is the tool list a turn offers now. A call can turn tools on
// (R-MCP-4a), so a provider that runs its own tool loop asks again after
// each call.
type ToolsFunc func() []Tool

// ChatRequest asks a model for the next step of a turn.
type ChatRequest struct {
	Model     string
	Effort    router.Effort // "" = the provider's default
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0: the provider's default
	// Exec runs the calls of a provider that runs its own tool loop. The
	// agent sets it whenever Tools is not empty; it runs each call on the
	// same path as an API model's tool_calls, and is safe for concurrent
	// use.
	Exec Exec
	// ToolsNow is Tools as it stands after the calls run so far, for a
	// provider that runs its own tool loop: it starts as Tools and grows
	// when a call turns tools on. The agent sets it with Exec; nil keeps
	// Tools for the whole call. It is safe for concurrent use.
	ToolsNow ToolsFunc
}

// ChatResponse is the model's reply: text, tool calls, or both.
type ChatResponse struct {
	Message Message
	Usage   Usage
}

// Provider is a model backend.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
