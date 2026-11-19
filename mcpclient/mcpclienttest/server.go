// Package mcpclienttest is a fake remote MCP server over Streamable HTTP
// for tests: it answers as JSON or as an event stream, issues or not a
// session id, expires it, pages its tool list, announces list changes on
// the GET stream, and records what it received. A test bends it through
// Options and Intercept; no test needs the network.
package mcpclienttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// T is what the fakes need from their owner: a test, or chatlab's
// program, which cannot implement testing.TB (ADR-0048).
type T interface {
	Helper()
	Cleanup(func())
}

// Options say how the server behaves; Update changes them while it runs.
type Options struct {
	// Tools are the raw tool definitions the list gives.
	Tools []json.RawMessage
	// PageSize is how many tools a page holds; zero is all in one page.
	PageSize int
	// Stream answers every POST as an event stream.
	Stream bool
	// Preamble are messages sent before the answer in an event stream (a
	// notification, a request of the server); it implies Stream.
	Preamble []string
	// SessionID makes the server issue a session id on initialize, this
	// text and a counter, and refuse a request that carries another with 404.
	SessionID string
	// Header and Value make the server refuse, with 401, a request that
	// does not carry this header with this value.
	Header, Value string
	// ListenStream serves the GET as an event stream; without it the GET
	// is 405.
	ListenStream bool
	// ProtocolVersion is the revision initialize answers; the default is
	// the newest.
	ProtocolVersion string
	// Call is the result object of tools/call; the default is a text
	// content "ok".
	Call func(name string, args json.RawMessage) any
	// Intercept sees every POST before the server does; true means it
	// answered, and the server does nothing more.
	Intercept func(w http.ResponseWriter, r *http.Request, req Request) bool
	// Bearer makes the server an OAuth protected resource: a request whose
	// bearer token Bearer does not accept gets 401 with Challenge as its
	// WWW-Authenticate header (none when Challenge is "").
	Bearer    func(token string) bool
	Challenge string
	// Documents are JSON documents served to a GET of their path under
	// /.well-known/, before any check: the resource's metadata (RFC 9728).
	// Another path there is 404.
	Documents map[string]string
}

// Request is a request the server received.
type Request struct {
	HTTPMethod string
	Header     http.Header
	Method     string // the JSON-RPC method, "" for a GET or a reply
	ID         json.RawMessage
	Params     json.RawMessage
	Body       []byte
}

// Server is the fake.
type Server struct {
	*httptest.Server
	done chan struct{}

	mu        sync.Mutex
	opts      Options
	reqs      []Request
	issued    int
	refused   int
	valid     map[string]bool
	listeners map[*listener]bool
	opened    chan struct{}
}

// New starts a server that stops with the test.
func New(t T, o Options) *Server {
	t.Helper()
	return start(t, o, httptest.NewServer)
}

// NewTLS starts a server over TLS that stops with the test. Every
// httptest TLS server has the same certificate, so the Client of one
// trusts all of them.
func NewTLS(t T, o Options) *Server {
	t.Helper()
	return start(t, o, httptest.NewTLSServer)
}

func start(t T, o Options, newServer func(http.Handler) *httptest.Server) *Server {
	t.Helper()
	s := &Server{opts: o, done: make(chan struct{}), valid: map[string]bool{},
		listeners: map[*listener]bool{}, opened: make(chan struct{}, 64)}
	s.Server = newServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		close(s.done)
		s.CloseClientConnections()
		s.Close()
	})
	return s
}

// Tool is a raw tool definition.
func Tool(name, description, schema string, readOnly bool) json.RawMessage {
	if schema == "" {
		schema = `{"type":"object","properties":{}}`
	}
	b, err := json.Marshal(map[string]any{"name": name, "description": description,
		"inputSchema": json.RawMessage(schema), "annotations": map[string]any{"readOnlyHint": readOnly}})
	if err != nil {
		panic(err)
	}
	return b
}

// Update changes the options.
func (s *Server) Update(f func(*Options)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.opts)
}

func (s *Server) options() Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

// Requests are the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Count is how many requests of the JSON-RPC method were received.
func (s *Server) Count(method string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// ExpireSessions makes every session id issued so far invalid: the next
// request that carries one gets 404.
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valid = map[string]bool{}
}

// listener is one open GET stream: msgs are written to it until gone is
// closed, when the stream has ended.
type listener struct {
	msgs chan string
	gone chan struct{}
}

// NotifyListChanged sends notifications/tools/list_changed on every GET
// stream open at the time of the call, and returns how many it reached. A
// stream that ends meanwhile is skipped; none is dropped for a full buffer.
func (s *Server) NotifyListChanged() int {
	s.mu.Lock()
	open := make([]*listener, 0, len(s.listeners))
	for l := range s.listeners {
		open = append(open, l)
	}
	s.mu.Unlock()
	n := 0
	for _, l := range open {
		select {
		case l.msgs <- `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`:
			n++
		case <-l.gone:
		case <-s.done:
		}
	}
	return n
}

// Opened gets a value each time a GET stream is opened, so a test waits for
// the listener instead of sleeping. The values queue up: a test that lets a
// stream open unread (a restart) reads that one first, and a notice sent
// then may reach a stream that is already closing.
func (s *Server) Opened() <-chan struct{} { return s.opened }

// Listeners is how many GET streams are open.
func (s *Server) Listeners() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.listeners)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	o := s.options()
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/.well-known/") {
		doc, ok := o.Documents[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, doc)
		return
	}
	if o.Header != "" && r.Header.Get(o.Header) != o.Value || !bearerOK(r, o) {
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		if o.Challenge != "" {
			w.Header().Set("WWW-Authenticate", o.Challenge)
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		s.serveGet(w, r, o)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return
	}
	req := Request{HTTPMethod: r.Method, Header: r.Header.Clone(), Body: body}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &m)
	req.ID, req.Method, req.Params = m.ID, m.Method, m.Params
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	if o.Intercept != nil && o.Intercept(w, r, req) {
		return
	}
	if !s.sessionOK(r, req.Method, o) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.dispatch(w, req, o)
}

// bearerOK reports whether the request carries a bearer token the server
// accepts, when it wants one.
func bearerOK(r *http.Request, o Options) bool {
	if o.Bearer == nil {
		return true
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && o.Bearer(token)
}

// Refused is how many requests the server answered with 401.
func (s *Server) Refused() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// sessionOK reports whether a request carries a session id the server still
// holds, when it issues them.
func (s *Server) sessionOK(r *http.Request, method string, o Options) bool {
	if o.SessionID == "" || method == "initialize" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.valid[r.Header.Get("Mcp-Session-Id")]
}

func (s *Server) dispatch(w http.ResponseWriter, req Request, o Options) {
	if len(bytes.TrimSpace(req.ID)) == 0 || req.Method == "" { // a notification, or a reply to the server
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		s.initialize(w, req, o)
	case "tools/list":
		s.list(w, req, o)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		var res any = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		if o.Call != nil {
			res = o.Call(p.Name, p.Arguments)
		}
		Answer(w, o, req.ID, res)
	case "ping":
		Answer(w, o, req.ID, map[string]any{})
	default:
		AnswerError(w, o, req.ID, -32601, "method not found")
	}
}

func (s *Server) initialize(w http.ResponseWriter, req Request, o Options) {
	v := o.ProtocolVersion
	if v == "" {
		v = "2025-11-25"
	}
	if o.SessionID != "" {
		s.mu.Lock()
		s.issued++
		id := o.SessionID + "-" + strconv.Itoa(s.issued)
		s.valid[id] = true
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", id)
	}
	Answer(w, o, req.ID, map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}},
		"serverInfo": map[string]any{"name": "fake", "version": "1"}})
}

func (s *Server) list(w http.ResponseWriter, req Request, o Options) {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(req.Params, &p)
	start, _ := strconv.Atoi(p.Cursor)
	tools := o.Tools
	res := map[string]any{}
	if o.PageSize > 0 && start < len(tools) {
		end := min(start+o.PageSize, len(tools))
		if end < len(tools) {
			res["nextCursor"] = strconv.Itoa(end)
		}
		tools = tools[start:end]
	}
	if tools == nil {
		tools = []json.RawMessage{}
	}
	res["tools"] = tools
	Answer(w, o, req.ID, res)
}

func (s *Server) serveGet(w http.ResponseWriter, r *http.Request, o Options) {
	if !o.ListenStream {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{HTTPMethod: r.Method, Header: r.Header.Clone()})
	valid := o.SessionID == "" || s.valid[r.Header.Get("Mcp-Session-Id")]
	s.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	l := &listener{msgs: make(chan string), gone: make(chan struct{})}
	s.mu.Lock()
	s.listeners[l] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, l)
		s.mu.Unlock()
		close(l.gone)
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	select {
	case s.opened <- struct{}{}:
	default:
	}
	for {
		select {
		case msg := <-l.msgs:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		}
	}
}

// Answer writes the result of request id as the options say: JSON, or an
// event stream that carries the Preamble first.
func Answer(w http.ResponseWriter, o Options, id json.RawMessage, result any) {
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		panic(err)
	}
	write(w, o, msg)
}

// AnswerError writes an error answer to request id.
func AnswerError(w http.ResponseWriter, o Options, id json.RawMessage, code int, message string) {
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	if err != nil {
		panic(err)
	}
	write(w, o, msg)
}

func write(w http.ResponseWriter, o Options, msg []byte) {
	if !o.Stream && len(o.Preamble) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(msg)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range o.Preamble {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", p)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
}
