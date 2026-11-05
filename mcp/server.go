// Package mcp is a minimal Model Context Protocol server: JSON-RPC 2.0,
// one message per line, over any io.ReadWriter (ADR-0037). It serves a
// fixed set of tools and runs their calls through a provider.Exec, so
// the caller's policies, taint, journal and call keys apply (R-PRV-6).
// The relay that carries it between a CLI's stdio and a Unix socket
// lives here too (ADR-0036).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/igorrius/ganglion/provider"
)

// protocolVersions are the MCP revisions the server speaks, newest
// first. They differ in nothing a tools-only server uses; a client asking
// for another gets the newest, as the lifecycle rules say.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// maxInFlight caps the tool calls running at once on one connection;
// the reader waits for a free slot.
const maxInFlight = 4

// ToolName is the MCP name of an agent tool: "." is not allowed in the
// names a model may call, so "schedule.create" is "schedule_create".
func ToolName(name string) string { return strings.ReplaceAll(name, ".", "_") }

// Option configures a Server.
type Option func(*Server)

// WithInstructions sets what the server tells its client in initialize.
func WithInstructions(text string) Option { return func(s *Server) { s.instructions = text } }

// Server serves tools over MCP.
type Server struct {
	name, version string
	instructions  string            // "" sends none
	tools         []provider.Tool   // in MCP names
	byMCP         map[string]string // MCP name → the agent's name
	exec          provider.Exec
}

// NewServer returns a server named name that offers tools and runs their
// calls with exec. Tool names are offered with "." mapped to "_"; of two
// tools whose MCP names collide, the first is offered.
func NewServer(name, version string, tools []provider.Tool, exec provider.Exec, opts ...Option) *Server {
	s := &Server{name: name, version: version, byMCP: make(map[string]string, len(tools)), exec: exec}
	for _, o := range opts {
		o(s)
	}
	for _, t := range tools {
		mcpName := ToolName(t.Name)
		if _, dup := s.byMCP[mcpName]; dup {
			continue
		}
		s.byMCP[mcpName] = t.Name
		t.Name = mcpName
		s.tools = append(s.tools, t)
	}
	return s
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// method answers one request; a nil *rpcError is success.
type method func(ctx context.Context, params json.RawMessage) (any, *rpcError)

// Serve answers requests read from rw until rw's reader ends, and
// returns nil then. Tool calls run concurrently, up to maxInFlight; when
// the reader ends, the calls still running are cancelled and Serve waits
// for them. A line longer than MaxLine stops it with ErrLineTooLong; a
// failed write stops it with that error. Serve does not watch ctx while
// it reads: the caller closes the connection to stop it.
func (s *Server) Serve(ctx context.Context, rw io.ReadWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait() // after cancel: deferred calls run last first
	defer cancel()
	w := &replies{w: rw}
	slots := make(chan struct{}, maxInFlight)
	lines := newLineReader(rw, MaxLine)
	for {
		line, err := lines.next()
		if errors.Is(err, io.EOF) {
			return w.failed()
		}
		if err != nil {
			return err
		}
		if err := w.failed(); err != nil {
			return err
		}
		req, ok, perr := parse(line)
		switch {
		case perr != nil:
			w.send(response{ID: req.ID, Error: perr})
		case !ok: // a notification, or a response to nothing we asked
		case req.Method == "tools/call":
			slots <- struct{}{}
			wg.Go(func() {
				defer func() { <-slots }()
				s.answer(ctx, w, req)
			})
		default:
			s.answer(ctx, w, req)
		}
	}
}

// parse decodes a line. ok is false for a message that gets no answer:
// a notification (no id) or a client's response. A blank line is
// skipped too.
func parse(line []byte) (request, bool, *rpcError) {
	null := request{ID: json.RawMessage("null")}
	if len(bytes.TrimSpace(line)) == 0 {
		return null, false, nil
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return null, false, &rpcError{Code: codeParse, Message: "parse error"}
	}
	if len(req.ID) == 0 {
		return req, false, nil
	}
	if req.Method == "" {
		return req, false, nil
	}
	return req, true, nil
}

// answer runs req's method and sends its response.
func (s *Server) answer(ctx context.Context, w *replies, req request) {
	m, ok := s.methods()[req.Method]
	if !ok {
		w.send(response{ID: req.ID, Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}})
		return
	}
	result, err := m(ctx, req.Params)
	w.send(response{ID: req.ID, Result: result, Error: err})
}

func (s *Server) methods() map[string]method {
	return map[string]method{
		"initialize": s.initialize,
		"ping":       func(context.Context, json.RawMessage) (any, *rpcError) { return struct{}{}, nil },
		"tools/list": s.list,
		"tools/call": s.call,
	}
}

func (s *Server) initialize(_ context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params"}
	}
	version := protocolVersions[0]
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": s.name, "version": s.version},
	}
	if s.instructions != "" {
		result["instructions"] = s.instructions
	}
	return result, nil
}

func (s *Server) list(context.Context, json.RawMessage) (any, *rpcError) {
	tools := s.tools
	if tools == nil {
		tools = []provider.Tool{}
	}
	return map[string]any{"tools": tools}, nil
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError"`
}

// call runs a tool. A name that is not offered goes to exec as it came:
// the core's policy refuses it and journals it, and a scheduled task's
// call outside its grant alerts the admins as on the API path. Only a
// call without a name is a protocol error. The tool's own error, and
// the policy's, is a result with isError, which the model reads. The
// arguments pass through as they came: the policy checks them against
// the tool's declared parameters (R18).
func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}
	}
	name, ok := s.byMCP[p.Name]
	if !ok {
		name = p.Name
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	text, err := s.exec(ctx, provider.ToolCall{Name: name, Args: args})
	if err != nil {
		return callResult{Content: []content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return callResult{Content: []content{{Type: "text", Text: text}}}, nil
}

// replies writes responses one line at a time from any goroutine and
// keeps the first write error.
type replies struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (r *replies) send(resp response) {
	resp.JSONRPC = "2.0"
	b, err := json.Marshal(resp)
	if err != nil { // a result is built from strings and raw JSON that was valid
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "unencodable result"}})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		_, r.err = r.w.Write(append(b, '\n'))
	}
}

func (r *replies) failed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
