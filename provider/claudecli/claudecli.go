// Package claudecli is a conversation provider that runs an unmodified
// `claude -p` per model call (R-PRV-4, ADR-0021). The core keeps the
// history (ADR-0020) and sends it on every call: there is no session and
// no --resume. The CLI uses its own login; this package never sees a
// token.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/provider"
)

// Client is a provider.Provider over the claude CLI.
type Client struct {
	name    string
	command string
	sem     chan struct{}

	dirOnce sync.Once
	dir     string
	dirErr  error
}

var _ provider.Provider = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithCommand sets the claude binary; the default is "claude" on PATH.
func WithCommand(path string) Option { return func(c *Client) { c.command = path } }

// WithMaxConcurrent caps the claude processes alive at once; default 2.
func WithMaxConcurrent(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.sem = make(chan struct{}, n)
		}
	}
}

// WithWorkDir sets the directory the CLI runs in; the default is a private
// temporary directory, so no project files or settings are picked up.
func WithWorkDir(dir string) Option {
	return func(c *Client) { c.dirOnce.Do(func() { c.dir = dir }) }
}

// New returns a client named name.
func New(name string, opts ...Option) *Client {
	c := &Client{name: name, command: "claude", sem: make(chan struct{}, 2)}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements provider.Provider.
func (c *Client) Name() string { return c.name }

func (c *Client) workDir() (string, error) {
	c.dirOnce.Do(func() { c.dir, c.dirErr = os.MkdirTemp("", "ganglion-claude-") })
	return c.dir, c.dirErr
}

// prompt splits the history into the system prompt and the transcript sent
// on stdin.
func prompt(msgs []provider.Message) (system, transcript string) {
	var sys, parts []string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			sys = append(sys, m.Content)
		case "user":
			parts = append(parts, "User: "+m.Content)
		case "assistant":
			if m.Content != "" {
				parts = append(parts, "Assistant: "+m.Content)
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, fmt.Sprintf("Assistant called tool %s with %s", tc.Name, tc.Args))
			}
		case "tool":
			parts = append(parts, "Tool result: "+m.Content)
		}
	}
	return strings.Join(sys, "\n\n"), "Conversation so far, oldest first:\n\n" + strings.Join(parts, "\n\n")
}

type result struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
}

// Chat implements provider.Provider.
func (c *Client) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if len(req.Tools) > 0 {
		return provider.ChatResponse{}, c.fail(provider.KindBadRequest, "tools arrive in M3b")
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return provider.ChatResponse{}, c.ctxError(ctx)
	}
	dir, err := c.workDir()
	if err != nil {
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "work dir: "+err.Error())
	}

	system, transcript := prompt(req.Messages)
	cmd := exec.CommandContext(ctx, c.command,
		"-p", "--model", req.Model, "--output-format", "json", "--no-session-persistence",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "",
		"--system-prompt", system)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(transcript) // never argv: argv shows in ps
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &limited{b: &stderr, n: 4096}
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()

	if ctx.Err() != nil {
		return provider.ChatResponse{}, c.ctxError(ctx)
	}
	if runErr != nil && (errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist)) {
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "claude not found: "+c.command)
	}
	var r result
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "no result: "+truncate(msg, 200))
	}
	if r.IsError {
		return provider.ChatResponse{}, c.fail(classify(r.Result), truncate(r.Result, 200))
	}
	u := r.Usage
	return provider.ChatResponse{
		Message: provider.Message{Role: "assistant", Content: r.Result},
		Usage: provider.Usage{
			InputTokens:      u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
			CacheReadTokens:  u.CacheReadInputTokens,
			CacheWriteTokens: u.CacheCreationInputTokens,
			OutputTokens:     u.OutputTokens,
		},
	}, nil
}

// classify maps the CLI's error text to an error kind.
func classify(text string) provider.ErrorKind {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "limit") && (strings.Contains(t, "usage") || strings.Contains(t, "rate") || strings.Contains(t, "reached")):
		return provider.KindRateLimit
	case strings.Contains(t, "login") || strings.Contains(t, "log in") || strings.Contains(t, "authenticat") || strings.Contains(t, "api key"):
		return provider.KindAuth
	}
	return provider.KindUnavailable
}

func (c *Client) ctxError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("provider %s: %w", c.name, context.Canceled)
	}
	return c.fail(provider.KindTimeout, "deadline exceeded")
}

func (c *Client) fail(kind provider.ErrorKind, msg string) error {
	return &provider.Error{Provider: c.name, Kind: kind, Msg: msg}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// limited keeps the first n bytes written and drops the rest.
type limited struct {
	b *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
