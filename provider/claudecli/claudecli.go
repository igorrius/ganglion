// Package claudecli is a conversation provider that runs an unmodified
// `claude -p` per model call (R-PRV-4, ADR-0021). The core keeps the
// history (ADR-0020) and sends it on every call: there is no session and
// no --resume. The CLI uses its own login; this package never sees a
// token.
//
// A call with tools runs the CLI's own tool loop over an MCP bridge to
// the core (R-PRV-6, ADR-0036): a per-run Unix socket in a private run
// dir, reached through `ganglion mcp-relay`. Every call goes through the
// request's Exec, so the core's policies, taint, journal and call keys
// apply; the CLI's built-in tools stay off.
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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/internal/procgroup"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/mcp"
	"github.com/igorrius/ganglion/provider"
)

// Client is a provider.Provider over the claude CLI.
type Client struct {
	name    string
	command string
	relay   string   // the binary with `mcp-relay`; "" is this executable
	pass    []string // more environment variables claude gets (WithEnv)
	sem     chan struct{}
	// reserve is one more slot, for runs without tools only: a run with
	// tools may wait on one without (a guard check of memory.write), so
	// runs with tools holding every slot cannot starve it (ruling R36).
	reserve chan struct{}

	fixedDir string // WithWorkDir; "" is a private temporary folder
	dirOnce  sync.Once
	dir      string
	dirErr   error
}

var _ provider.Provider = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithCommand sets the claude binary; the default, and "", is "claude" on
// PATH.
func WithCommand(path string) Option {
	return func(c *Client) {
		if path != "" {
			c.command = path
		}
	}
}

// WithRelay sets the binary the CLI starts as the bridge's MCP server,
// with the arguments `mcp-relay --socket <path>`; the default, and "",
// is this executable.
func WithRelay(path string) Option {
	return func(c *Client) { c.relay = path }
}

// WithEnv names more environment variables claude gets besides the ones
// it needs to run and stay logged in (passedEnv), such as a proxy setting
// of the operator's own.
func WithEnv(names ...string) Option {
	return func(c *Client) { c.pass = append(c.pass, names...) }
}

// WithMaxConcurrent caps the claude processes alive at once; default 2.
// A run without tools may also take one reserved slot, so at most n+1
// run at once.
func WithMaxConcurrent(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.sem = make(chan struct{}, n)
		}
	}
}

// WithWorkDir sets the folder the CLI runs in, made 0700 when first used;
// the default is a private temporary folder, so no project files or
// settings are picked up. The node passes one under its data folder, which
// its places count and its janitor cleans (R-CMD-8).
func WithWorkDir(dir string) Option { return func(c *Client) { c.fixedDir = dir } }

// New returns a client named name.
func New(name string, opts ...Option) *Client {
	c := &Client{name: name, command: "claude", sem: make(chan struct{}, 2), reserve: make(chan struct{}, 1)}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements provider.Provider.
func (c *Client) Name() string { return c.name }

func (c *Client) workDir() (string, error) {
	c.dirOnce.Do(func() {
		if c.fixedDir != "" {
			// Absolute, because the CLI runs inside this folder and would
			// resolve a relative prompt file path from there.
			if c.dir, c.dirErr = filepath.Abs(c.fixedDir); c.dirErr == nil {
				c.dirErr = os.MkdirAll(c.dir, 0o700)
			}
			return
		}
		c.dir, c.dirErr = os.MkdirTemp("", "ganglion-claude-")
	})
	return c.dir, c.dirErr
}

// UsageLimitCooldown is the RetryAfter of a subscription usage limit: such
// limits last hours, so the router should not try the CLI again every
// minute.
const UsageLimitCooldown = 15 * time.Minute

// prompt splits the history into the system prompt and the text sent on
// stdin. Only the first system message (the operator's prompt with the
// memory's index) is the system prompt, which goes to a 0600 file
// (--system-prompt-file): argv shows in ps. Later system messages, such
// as the conversation summary, hold family text and go to stdin ahead of
// the transcript.
func prompt(msgs []provider.Message) (system, stdin string) {
	var head []string
	seenSystem := false
	for _, m := range msgs {
		switch {
		case m.Role != provider.RoleSystem:
		case !seenSystem:
			system, seenSystem = m.Content, true
		default:
			head = append(head, transcript.Indent(m.Content)) // a summary is family text too
		}
	}
	parts := transcript.Lines(msgs, transcript.WithToolCalls)
	stdin = "Conversation so far, oldest first:\n\n" + strings.Join(parts, "\n\n")
	if len(head) > 0 {
		stdin = strings.Join(head, "\n\n") + "\n\n" + stdin
	}
	return system, stdin
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

// Chat implements provider.Provider. A request with tools serves them
// to the run over the MCP bridge and runs each call through req.Exec;
// the run dir is removed whatever way the run ends.
func (c *Client) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if len(req.Tools) > 0 && req.Exec == nil {
		return provider.ChatResponse{}, c.fail(provider.KindBadRequest, "tools without an executor")
	}
	release, err := c.acquire(ctx, len(req.Tools) > 0)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	defer release()
	dir, err := c.workDir()
	if err != nil {
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "work dir: "+err.Error())
	}

	system, transcript := prompt(req.Messages)
	systemFile, cleanup, err := writeSystem(dir, system)
	if err != nil {
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "system prompt: "+err.Error())
	}
	defer cleanup()
	args := []string{"-p", "--model", req.Model}
	if req.Effort != "" {
		args = append(args, "--effort", string(req.Effort))
	}
	args = append(args, "--output-format", "json", "--no-session-persistence", "--tools", "", "--strict-mcp-config")
	if len(req.Tools) > 0 {
		b, err := c.bridge(ctx, dir, req)
		if err != nil {
			return provider.ChatResponse{}, err
		}
		defer b.close()
		args = append(args, b.args()...)
	}
	args = append(args, "--setting-sources", "", "--system-prompt-file", systemFile)
	stdout, stderr, runErr := c.run(ctx, dir, args, transcript)
	return c.result(ctx, stdout, stderr, runErr)
}

// acquire takes a slot for one run and returns its release: a run with
// tools waits for one of the shared slots; a run without tools takes a
// shared slot or the reserved one, whichever is free first.
func (c *Client) acquire(ctx context.Context, tools bool) (func(), error) {
	reserve := c.reserve
	if tools {
		reserve = nil // a nil channel is never ready
	}
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case reserve <- struct{}{}:
		return func() { <-c.reserve }, nil
	case <-ctx.Done():
		return nil, c.ctxError(ctx)
	}
}

// systemName is the system prompt's file in its own dir.
const systemName = "system.md"

// writeSystem writes the system prompt to a 0600 file in a new 0700 dir
// under dir, and returns its path and a cleanup that removes the dir. The
// prompt holds the memory's index, so it never goes to argv, which every
// local user can read in /proc.
func writeSystem(dir, system string) (string, func(), error) {
	d, err := os.MkdirTemp(dir, "prompt-") // mode 0700
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(d) }
	path := filepath.Join(d, systemName)
	if err := os.WriteFile(path, []byte(system), privateMode); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// passedEnv are the variables claude gets from this process's
// environment: what it needs to run (PATH, HOME, the locale, temporary
// and XDG directories, proxies and CA certificates) and its own login and
// settings (CLAUDE_*, ANTHROPIC_*, DISABLE_*). Nothing else: the node's
// own secrets (TELEGRAM_BOT_TOKEN, OPENROUTER_API_KEY, BRAVE_API_KEY and
// their *_FILE) stay out, and the relay's token reaches only the relay,
// through mcp.json.
var passedEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TZ", "TMPDIR", "TERM",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// passedPrefixes are the variable name prefixes claude gets (passedEnv).
var passedPrefixes = []string{"LC_", "XDG_", "CLAUDE_", "ANTHROPIC_", "DISABLE_"}

// env is claude's environment: the passed variables of this process's
// environment, and the ones named with WithEnv.
func (c *Client) env() []string {
	out := []string{} // never nil: a nil Cmd.Env inherits everything
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(passedEnv, name) || slices.Contains(c.pass, name) || hasAnyPrefix(name, passedPrefixes) {
			out = append(out, kv)
		}
	}
	return out
}

// hasAnyPrefix reports whether s starts with one of prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(s, p) })
}

// bridge opens the MCP bridge that serves req's tools to one run.
func (c *Client) bridge(ctx context.Context, dir string, req provider.ChatRequest) (*bridge, error) {
	relay := c.relay
	if relay == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, c.fail(provider.KindUnavailable, "mcp bridge: relay: "+err.Error())
		}
		relay = exe
	}
	server := mcp.NewServer(serverName, buildinfo.Read().Version, req.Tools, req.Exec)
	b, err := openBridge(ctx, dir, relay, authTimeout, server)
	if err != nil {
		return nil, c.fail(provider.KindUnavailable, "mcp bridge: "+err.Error())
	}
	return b, nil
}

// run runs claude with args in dir, the transcript on stdin, and returns
// its output.
func (c *Client) run(ctx context.Context, dir string, args []string, stdin string) (stdout, stderr bytes.Buffer, err error) {
	cmd := exec.CommandContext(ctx, c.command, args...)
	cmd.Dir = dir
	cmd.Env = c.env()
	cmd.Stdin = strings.NewReader(stdin) // never argv: argv shows in ps
	cmd.Stdout, cmd.Stderr = &stdout, &limited{b: &stderr, n: 4096}
	cmd.WaitDelay = 2 * time.Second
	procgroup.Own(cmd)
	err = cmd.Run()
	return stdout, stderr, err
}

// result reads the CLI's answer from its output.
func (c *Client) result(ctx context.Context, stdout, stderr bytes.Buffer, runErr error) (provider.ChatResponse, error) {
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
		return provider.ChatResponse{}, c.fail(provider.KindUnavailable, "no result: "+text.Truncate(msg, 200))
	}
	if r.IsError {
		kind := classify(r.Result)
		e := &provider.Error{Provider: c.name, Kind: kind, Msg: text.Truncate(r.Result, 200)}
		if kind == provider.KindRateLimit {
			e.RetryAfter = UsageLimitCooldown
		}
		return provider.ChatResponse{}, e
	}
	u := r.Usage
	return provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: r.Result},
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
