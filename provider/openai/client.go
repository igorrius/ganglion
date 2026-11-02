// Package openai is a provider over the OpenAI-compatible Chat Completions
// API: OpenAI, OpenRouter, DeepSeek, Ollama and the like (R-PRV-1,
// ADR-0007). Wire types stay in this package; callers see the neutral
// provider types (ADR-0020).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/provider"
)

// DefaultTimeout bounds one call when no HTTP client is given.
const DefaultTimeout = 90 * time.Second

const maxResponse = 10 << 20

// Client is a provider.Provider over Chat Completions.
type Client struct {
	name string
	base string
	key  string
	hc   *http.Client
}

var _ provider.Provider = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// New returns a client named name for the API at baseURL (for example
// https://openrouter.ai/api/v1). apiKey may be empty for a local server.
func New(name, baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{name: name, base: strings.TrimRight(baseURL, "/"), key: apiKey, hc: &http.Client{Timeout: DefaultTimeout}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements provider.Provider.
func (c *Client) Name() string { return c.name }

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func toWire(req provider.ChatRequest) wireRequest {
	w := wireRequest{Model: req.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		content := m.Content
		wm := wireMessage{Role: string(m.Role), Content: &content, ToolCallID: m.ToolCallID}
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 && content == "" {
			wm.Content = nil
		}
		for _, tc := range m.ToolCalls {
			var wt wireToolCall
			wt.ID, wt.Type = tc.ID, "function"
			wt.Function.Name = tc.Name
			wt.Function.Arguments = string(tc.Args)
			if wt.Function.Arguments == "" {
				wt.Function.Arguments = "{}"
			}
			wm.ToolCalls = append(wm.ToolCalls, wt)
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name, wt.Function.Description, wt.Function.Parameters = t.Name, t.Description, t.InputSchema
		if len(wt.Function.Parameters) == 0 {
			wt.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		w.Tools = append(w.Tools, wt)
	}
	return w
}

// Chat implements provider.Provider.
func (c *Client) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	body, err := json.Marshal(toWire(req))
	if err != nil {
		return provider.ChatResponse{}, c.fail(provider.KindBadRequest, 0, 0, err.Error())
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return provider.ChatResponse{}, c.fail(provider.KindBadRequest, 0, 0, err.Error())
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.hc.Do(hreq)
	if err != nil {
		return provider.ChatResponse{}, c.transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return provider.ChatResponse{}, c.transportError(ctx, err)
	}

	var wr wireResponse
	decodeErr := json.Unmarshal(raw, &wr)
	if resp.StatusCode != http.StatusOK {
		msg := http.StatusText(resp.StatusCode)
		if decodeErr == nil && wr.Error != nil && wr.Error.Message != "" {
			msg = wr.Error.Message
		}
		retry := 0
		if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && v > 0 {
			retry = v
		}
		return provider.ChatResponse{}, c.fail(kindOf(resp.StatusCode), resp.StatusCode, time.Duration(retry)*time.Second, msg)
	}
	if decodeErr != nil || len(wr.Choices) == 0 {
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, resp.StatusCode, 0, "unreadable answer")
	}
	wm := wr.Choices[0].Message
	out := provider.Message{Role: provider.RoleAssistant}
	if wm.Content != nil {
		out.Content = *wm.Content
	}
	for _, tc := range wm.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage(strconv.Quote(tc.Function.Arguments))
		}
		out.ToolCalls = append(out.ToolCalls, provider.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	return provider.ChatResponse{Message: out, Usage: provider.Usage{
		InputTokens:     wr.Usage.PromptTokens,
		CacheReadTokens: wr.Usage.PromptTokensDetails.CachedTokens,
		OutputTokens:    wr.Usage.CompletionTokens,
	}}, nil
}

func kindOf(status int) provider.ErrorKind {
	switch {
	case status == http.StatusTooManyRequests:
		return provider.KindRateLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.KindAuth
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return provider.KindTimeout
	case status >= 500:
		return provider.KindUnavailable
	default:
		return provider.KindBadRequest
	}
}

// fail builds a provider error whose message has the key redacted.
func (c *Client) fail(kind provider.ErrorKind, status int, retry time.Duration, msg string) error {
	return &provider.Error{Provider: c.name, Kind: kind, Status: status, RetryAfter: retry, Msg: c.redact(msg)}
}

// transportError maps a failure to reach the API. A cancelled turn stays
// context.Canceled so the caller does not fall back; a deadline is a
// timeout.
func (c *Client) transportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("provider %s: %w", c.name, context.Canceled)
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return c.fail(provider.KindTimeout, 0, 0, "deadline exceeded")
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return c.fail(provider.KindTimeout, 0, 0, "timeout")
	}
	return c.fail(provider.KindUnavailable, 0, 0, err.Error())
}

func (c *Client) redact(s string) string {
	if c.key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.key, "<key>")
}
