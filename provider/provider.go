// Package provider defines model backends: an OpenAI-compatible client
// first (which also covers Ollama), the Anthropic Messages API later, and
// the claude CLI worker (docs/spec.md, section 7.4).
package provider

import (
	"context"
	"encoding/json"
	"time"
)

// ModelCaps are the tags the router filters and ranks models by
// (R-RTR-1).
type ModelCaps struct {
	Tools         bool
	Vision        bool
	ContextTokens int
	Local         bool    // data stays on the user's hardware
	PriceInMTok   float64 // USD per million input tokens
	PriceOutMTok  float64 // USD per million output tokens
	Latency       time.Duration
}

// Message is one entry of the provider-neutral history the core keeps
// (R-AGT-2).
type Message struct {
	Role    string // "system", "user", "assistant", "tool"
	Content string
}

// Usage is what a call cost, recorded in the turns table.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ChatRequest asks a model for the next step of a turn.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []json.RawMessage // MCP tool schemas; the provider translates them (R-PRV-3)
}

// ChatResponse is the model's reply.
type ChatResponse struct {
	Message Message
	Usage   Usage
}

// Provider is a model backend.
type Provider interface {
	Name() string
	Caps() ModelCaps
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
