// Package mcpclient is the node's client of remote MCP servers over
// Streamable HTTP and the servers' tools in the agent's registry
// (docs/spec.md, 7.16; ADR-0097). The client speaks initialize, tools/list,
// tools/call and notifications/tools/list_changed, declares no capability
// and reads every answer within a limit. The Hub keeps the connected
// servers, pins each tool list by a fingerprint and offers the approved
// tools to a session only on demand (R-MCP-4a).
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/mcp"
)

// ProtocolVersion is the revision of the protocol the client asks for. An
// answer with another revision of mcp.ProtocolVersions is accepted.
const ProtocolVersion = "2025-11-25"

// DefaultTimeout is how long one request to a server may take (R-MCP-2).
const DefaultTimeout = 30 * time.Second

// Limits of what the client reads.
const (
	// MaxInitBytes is the biggest answer to initialize.
	MaxInitBytes = 64 << 10
	// MaxPages is the most pages of one tool list.
	MaxPages = 32
	// maxCursor is the longest cursor of a list page, in bytes.
	maxCursor = 1 << 10
	// maxRedirects is how many redirects one request follows.
	maxRedirects = 3
	// maxDrain is how much of a body is read to free its connection.
	maxDrain = 64 << 10
	// maxReplies is how many requests of the server one stream is answered for.
	maxReplies = 16
	// maxSessionID is the longest session id taken.
	maxSessionID = 256
)

// Headers of the protocol.
const (
	headerSession  = "Mcp-Session-Id"
	headerProtocol = "MCP-Protocol-Version"
	methodListed   = "notifications/tools/list_changed"
)

// ListChangedFunc is told that the server's tool list changed. It is called
// from the goroutine that reads the stream, so it must not block.
type ListChangedFunc func()

// Client is the client of one server. It is safe for concurrent use: calls
// share one session, which a new initialize replaces when the server drops
// it.
type Client struct {
	name     string
	rawURL   string
	origin   origin
	auth     Auth
	hc       *http.Client
	timeout  time.Duration
	version  string
	onChange ListChangedFunc

	nextID  atomic.Int64
	ignored atomic.Int64

	initMu sync.Mutex // one initialize at a time
	mu     sync.Mutex // guards the session
	ready  bool
	sid    string
	proto  string // the revision initialize agreed on; "" before it
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient sets the HTTP client; its redirect policy is replaced by
// the client's own (R-MCP-5). The default has no timeout of its own: every
// request runs under the server's timeout.
func WithHTTPClient(hc *http.Client) ClientOption { return func(c *Client) { c.hc = hc } }

// WithVersion sets the version clientInfo reports.
func WithVersion(v string) ClientOption { return func(c *Client) { c.version = v } }

// WithListChanged sets who is told of notifications/tools/list_changed.
func WithListChanged(f ListChangedFunc) ClientOption { return func(c *Client) { c.onChange = f } }

// NewClient makes the client of the server called name at rawURL, which
// sends auth's header with each request and gives up on a request after
// timeout. name only labels errors.
func NewClient(name, rawURL string, auth Auth, timeout time.Duration, opts ...ClientOption) *Client {
	c := &Client{name: name, rawURL: rawURL, auth: auth, timeout: timeout, version: "dev",
		hc: &http.Client{}, onChange: func() {}}
	for _, o := range opts {
		o(c)
	}
	hc := *c.hc
	hc.CheckRedirect = c.checkRedirect
	c.hc = &hc
	if u, err := url.Parse(rawURL); err == nil {
		c.origin = originOf(u)
	}
	return c
}

// Ignored is how many messages the client dropped: malformed ones, answers
// to nothing, requests past the per-stream limit.
func (c *Client) Ignored() int64 { return c.ignored.Load() }

// bound derives the context of one operation, with the server's timeout.
func (c *Client) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.timeout)
}

// wrap names the action an error came from.
func (c *Client) wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mcpclient: %s %s: %w", action, c.name, err)
}

// Initialize starts a session: initialize, then notifications/initialized.
// It replaces any session the client held.
func (c *Client) Initialize(ctx context.Context) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	c.initMu.Lock()
	defer c.initMu.Unlock()
	return c.wrap("initialize", c.initialize(ctx))
}

// reset forgets the session.
func (c *Client) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready, c.sid, c.proto = false, "", ""
}

// session is the session id held, "" for none.
func (c *Client) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid
}

// agreed is the session id and the protocol revision every request after
// initialize carries; both "" before it.
func (c *Client) agreed() (sid, proto string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid, c.proto
}

// ensure starts a session when there is none.
func (c *Client) ensure(ctx context.Context) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if ready {
		return nil
	}
	return c.initialize(ctx)
}

// renew starts a new session after the server dropped stale; another call
// may have done so already.
func (c *Client) renew(ctx context.Context, stale string) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.session() != stale {
		return nil
	}
	return c.initialize(ctx)
}

// initParams are the parameters of initialize: no client capability.
type initParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    struct{}       `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
}

// initialize runs the handshake, dropping any session first. The caller
// holds initMu.
func (c *Client) initialize(ctx context.Context) error {
	c.reset()
	p := initParams{ProtocolVersion: ProtocolVersion, ClientInfo: map[string]any{"name": "ganglion", "version": c.version}}
	a, err := c.exchange(ctx, "initialize", p, MaxInitBytes)
	if err != nil {
		return err
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(a.result, &res) != nil || !versionKnown(res.ProtocolVersion) {
		return fmt.Errorf("%w: unsupported protocol revision", ErrProtocol)
	}
	sid := a.header.Get(headerSession)
	if !validSessionID(sid) {
		return fmt.Errorf("%w: bad session id", ErrProtocol)
	}
	c.mu.Lock()
	c.sid, c.proto = sid, res.ProtocolVersion
	c.mu.Unlock()
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return err
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return nil
}

// versionKnown reports whether v is a revision the node speaks.
func versionKnown(v string) bool {
	return slices.Contains(mcp.ProtocolVersions(), v)
}

// validSessionID reports whether s is no session id or a visible ASCII
// string of at most maxSessionID bytes.
func validSessionID(s string) bool {
	if len(s) > maxSessionID {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// notify sends a notification; the server accepts it with 202 or 200.
func (c *Client) notify(ctx context.Context, method string) error {
	body, err := json.Marshal(request{JSONRPC: jsonrpcVersion, Method: method})
	if err != nil {
		return fmt.Errorf("mcpclient: encode %s: %w", method, err)
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	drainClose(resp.Body)
	return nil
}

// answer is a request's result and the headers it came with.
type answer struct {
	result json.RawMessage
	header http.Header
}

// call runs one request with the session, starting one when there is none
// and starting a new one once when the server dropped it: the dropped
// session did not process the request, so repeating it is safe for any
// call (R-MCP-1).
func (c *Client) call(ctx context.Context, method string, params any, limit int64) (json.RawMessage, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	stale := c.session()
	a, err := c.exchange(ctx, method, params, limit)
	if !errors.Is(err, ErrSessionExpired) {
		return a.result, err
	}
	if err := c.renew(ctx, stale); err != nil {
		return nil, err
	}
	a, err = c.exchange(ctx, method, params, limit)
	return a.result, err
}

// exchange sends one request and reads its answer, within limit bytes.
func (c *Client) exchange(ctx context.Context, method string, params any, limit int64) (answer, error) {
	id := c.nextID.Add(1)
	body, err := json.Marshal(request{JSONRPC: jsonrpcVersion, ID: &id, Method: method, Params: params})
	if err != nil {
		return answer{}, fmt.Errorf("mcpclient: encode %s: %w", method, err)
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return answer{}, err
	}
	defer drainClose(resp.Body)
	res, err := c.read(ctx, resp, id, limit)
	return answer{result: res, header: resp.Header}, err
}

// read takes the answer to request id from a response: one JSON message, or
// an event stream until the message with the id comes.
func (c *Client) read(ctx context.Context, resp *http.Response, id, limit int64) (json.RawMessage, error) {
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, ErrNoAnswer
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "text/event-stream":
		return c.readStream(ctx, resp.Body, id, limit)
	case "application/json":
		return c.readJSON(ctx, resp.Body, id, limit)
	}
	return nil, fmt.Errorf("%w: the answer is neither JSON nor an event stream", ErrProtocol)
}

// readJSON takes the answer from a body of one JSON message.
func (c *Client) readJSON(ctx context.Context, body io.Reader, id, limit int64) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, c.failure(ctx, err, false)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, limit)
	}
	replies := 0
	res, done, err := c.handle(ctx, data, id, &replies)
	if err != nil || done {
		return res, err
	}
	return nil, ErrNoAnswer
}

// readStream reads events until the answer to request id comes. The whole
// stream is capped at limit plus a slack; a stream that ends first is
// ErrNoAnswer.
func (c *Client) readStream(ctx context.Context, body io.Reader, id, limit int64) (json.RawMessage, error) {
	events := newEventReader(&capReader{r: body, left: limit + streamSlack}, int(limit))
	replies := 0
	for {
		ev, err := events.next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNoAnswer
		}
		if err != nil {
			return nil, c.streamFailure(ctx, err)
		}
		res, done, err := c.handle(ctx, ev.data, id, &replies)
		if err != nil || done {
			return res, err
		}
	}
}

// streamFailure is a failed read of a stream: our own errors stay, the
// transport's become ours.
func (c *Client) streamFailure(ctx context.Context, err error) error {
	if errors.Is(err, ErrTooLarge) {
		return err
	}
	return c.failure(ctx, err, false)
}

// handle takes one message of the server. done is true when it is the
// answer to request id (res, or the *RPCError as err). A notification is
// passed on or ignored; a request of the server is refused and never
// acted on (R-MCP-1); anything else is dropped and counted.
func (c *Client) handle(ctx context.Context, data []byte, id int64, replies *int) (res json.RawMessage, done bool, err error) {
	var m message
	if json.Unmarshal(data, &m) != nil || m.JSONRPC != jsonrpcVersion {
		c.ignored.Add(1)
		return nil, false, nil
	}
	switch {
	case m.Method != "" && m.hasID():
		c.refuse(ctx, m, replies)
	case m.Method == methodListed:
		c.onChange()
	case m.Method != "":
		// Progress, log messages and the like: nothing the node uses.
	case m.hasID() && m.answers(id):
		return answerOf(m)
	default:
		c.ignored.Add(1)
	}
	return nil, false, nil
}

// answerOf is the outcome of the answer message m.
func answerOf(m message) (json.RawMessage, bool, error) {
	if m.Error != nil {
		return nil, true, cutError(m.Error)
	}
	if len(m.Result) == 0 {
		return nil, true, fmt.Errorf("%w: an answer without a result", ErrProtocol)
	}
	return m.Result, true, nil
}

// refuse answers a request of the server: ping with an empty result,
// everything else (sampling, elicitation, roots) with "method not
// supported". At most maxReplies a stream, and never to an id that is long:
// the answer repeats it. A failed answer is dropped.
func (c *Client) refuse(ctx context.Context, m message, replies *int) {
	if *replies >= maxReplies || len(m.ID) > maxEchoedID {
		c.ignored.Add(1)
		return
	}
	*replies++
	r := reply{JSONRPC: jsonrpcVersion, ID: m.ID}
	if m.Method == "ping" {
		r.Result = struct{}{}
	} else {
		r.Error = &RPCError{Code: codeMethodNotFound, Message: "method not supported"}
	}
	body, err := json.Marshal(r)
	if err != nil {
		c.ignored.Add(1)
		return
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		c.ignored.Add(1)
		return
	}
	drainClose(resp.Body)
}

// ToolList is the server's tools as listed.
type ToolList struct {
	Listed
	Fingerprint string // of every listed tool, the skipped ones too (R-MCP-7)
}

// ListTools lists the server's tools, following the pages. The whole list
// is at most MaxListBytes and MaxPages pages; past either, or on a cursor
// that repeats, it is an error and nothing is taken.
func (c *Client) ListTools(ctx context.Context) (ToolList, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	raw, err := c.listPages(ctx)
	if err != nil {
		return ToolList{}, c.wrap("list tools of", err)
	}
	return ToolList{Listed: Parse(c.name, raw), Fingerprint: Fingerprint(raw)}, nil
}

// listPages is the raw entries of every page.
func (c *Client) listPages(ctx context.Context) ([]json.RawMessage, error) {
	var all []json.RawMessage
	seen := map[string]bool{}
	cursor, total := "", 0
	for range MaxPages {
		var params map[string]string
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		res, err := c.call(ctx, "tools/list", params, MaxListBytes)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if json.Unmarshal(res, &page) != nil {
			return nil, fmt.Errorf("%w: a malformed page", ErrProtocol)
		}
		for _, t := range page.Tools {
			if total += len(t); total > MaxListBytes {
				return nil, fmt.Errorf("%w: the tool list is over %d bytes", ErrTooLarge, MaxListBytes)
			}
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		if len(page.NextCursor) > maxCursor || seen[page.NextCursor] {
			return nil, fmt.Errorf("%w: a bad or repeated cursor", ErrProtocol)
		}
		seen[page.NextCursor], cursor = true, page.NextCursor
	}
	return nil, fmt.Errorf("%w: more than %d pages", ErrProtocol, MaxPages)
}

// CallTool calls the server's tool own with args, a JSON object ("" or
// null is none). A JSON-RPC error answer is an *RPCError. The call is never
// repeated except after a dropped session, which did not process it.
func (c *Client) CallTool(ctx context.Context, own string, args json.RawMessage) (CallResult, error) {
	args, err := objectOrEmpty(args)
	if err != nil {
		return CallResult{}, err
	}
	ctx, cancel := c.bound(ctx)
	defer cancel()
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{own, args}
	res, err := c.call(ctx, "tools/call", params, MaxCallBytes)
	if err != nil {
		return CallResult{}, c.wrap("call a tool of", err)
	}
	return parseResult(res)
}

// objectOrEmpty is args as a JSON object; none is {}.
func objectOrEmpty(args json.RawMessage) (json.RawMessage, error) {
	t := bytes.TrimSpace(args)
	if len(t) == 0 || string(t) == jsonNull {
		return json.RawMessage(`{}`), nil
	}
	if t[0] != '{' || !json.Valid(t) {
		return nil, ErrBadArguments
	}
	return t, nil
}

// Listen reads the server's own stream (a GET) until it ends or ctx does,
// telling the ListChangedFunc of tool list changes. A server without such a
// stream is ErrNoStream: the caller stops listening and does not retry. A
// nil error is a stream that ended; only each event is bounded.
func (c *Client) Listen(ctx context.Context) error {
	if err := c.ensureWithin(ctx); err != nil {
		return c.wrap("listen to", err)
	}
	resp, err := c.openStream(ctx)
	if err != nil {
		return c.wrap("listen to", err)
	}
	defer drainClose(resp.Body)
	events := newEventReader(resp.Body, maxListenEvent)
	replies := 0
	for {
		ev, err := events.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return c.wrap("listen to", c.streamFailure(ctx, err))
		}
		if _, _, err := c.handle(ctx, ev.data, -1, &replies); err != nil {
			return c.wrap("listen to", err)
		}
	}
}

// ensureWithin starts a session under the server's timeout.
func (c *Client) ensureWithin(ctx context.Context) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	return c.ensure(ctx)
}

// openStream makes the GET and checks that an event stream answers. Only
// its headers have the server's timeout.
func (c *Client) openStream(ctx context.Context) (*http.Response, error) {
	gctx, cancel := context.WithCancelCause(ctx)
	stop := time.AfterFunc(c.timeout, func() { cancel(context.DeadlineExceeded) })
	req, err := http.NewRequestWithContext(gctx, http.MethodGet, c.rawURL, http.NoBody)
	if err != nil {
		stop.Stop()
		cancel(nil)
		return nil, fmt.Errorf("mcpclient: build the request: %w", ErrProtocol)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.send(gctx, req)
	stop.Stop()
	if err != nil {
		cancel(nil)
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusMethodNotAllowed {
			return nil, ErrNoStream
		}
		return nil, err
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/event-stream" {
		drainClose(resp.Body)
		cancel(nil)
		return nil, ErrNoStream
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: func() { cancel(nil) }}
	return resp, nil
}

// cancelBody releases its request's context with the body.
type cancelBody struct {
	io.ReadCloser
	cancel func()
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// post sends a JSON-RPC message.
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcpclient: build the request: %w", ErrProtocol)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return c.send(ctx, req)
}

// authHeaderKey carries the name of the credential header in the request's
// context, so a redirect can drop it (R-MCP-5).
type authHeaderKey struct{}

// send adds the session, the protocol revision agreed in initialize and
// the credential to req, sends it and checks the status. A server without
// sessions still gets the revision on every request after initialize. The
// error never holds the address.
func (c *Client) send(ctx context.Context, req *http.Request) (*http.Response, error) {
	sid, proto := c.agreed()
	if sid != "" {
		req.Header.Set(headerSession, sid)
	}
	if proto != "" {
		req.Header.Set(headerProtocol, proto)
	}
	req, err := c.authorize(ctx, req)
	if err != nil {
		return nil, err
	}
	var connected atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }}
	resp, err := c.hc.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		return nil, c.failure(ctx, err, !connected.Load())
	}
	return checkStatus(resp, sid != "")
}

// authorize adds the credential header.
func (c *Client) authorize(ctx context.Context, req *http.Request) (*http.Request, error) {
	name, value, err := c.auth.Header(ctx)
	if err != nil {
		return nil, &credentialError{server: c.name, cause: err}
	}
	if name == "" {
		return req, nil
	}
	req.Header.Set(name, value)
	return req.WithContext(context.WithValue(req.Context(), authHeaderKey{}, name)), nil
}

// checkStatus passes a success and turns the other statuses into errors;
// the body of an error is not read.
func checkStatus(resp *http.Response, hadSession bool) (*http.Response, error) {
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		return resp, nil
	}
	drainClose(resp.Body)
	switch {
	case code == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case code == http.StatusForbidden:
		return nil, ErrForbidden
	case code == http.StatusNotFound && hadSession:
		return nil, ErrSessionExpired
	}
	return nil, &StatusError{Code: code}
}

// failure is a request that failed in the transport, in the client's own
// words. notSent says no connection was made for it.
func (c *Client) failure(ctx context.Context, err error, notSent bool) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(err, ErrRedirect):
		return ErrRedirect
	case cause != nil && errors.Is(cause, context.DeadlineExceeded):
		return &transportError{text: "the server did not answer in time", timeout: true, notSent: notSent}
	case cause != nil:
		return fmt.Errorf("mcpclient: cancelled: %w", cause)
	}
	return &transportError{text: "the server is unreachable", notSent: notSent}
}

// drainClose reads what is left of a body, up to a limit, and closes it, so
// the connection can be used again.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrain))
	_ = body.Close()
}

// origin is where a server is: scheme, host and port, with the default
// port filled in.
type origin struct{ scheme, host, port string }

// originOf is the origin of u.
func originOf(u *url.URL) origin {
	o := origin{scheme: strings.ToLower(u.Scheme), host: strings.ToLower(u.Hostname()), port: u.Port()}
	if o.port == "" {
		o.port = map[string]string{"http": "80", "https": "443"}[o.scheme]
	}
	return o
}

// checkRedirect is the client's redirect policy (R-MCP-5): at most
// maxRedirects, never from https to http, and the credential and the
// session id go only to the server's origin.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return ErrRedirect
	}
	if req.URL.Scheme == "http" && via[len(via)-1].URL.Scheme == "https" {
		return ErrRedirect
	}
	if originOf(req.URL) != c.origin {
		req.Header.Del(headerSession)
		if name, ok := req.Context().Value(authHeaderKey{}).(string); ok {
			req.Header.Del(name)
		}
	}
	return nil
}
