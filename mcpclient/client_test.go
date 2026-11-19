package mcpclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
)

const testTimeout = 5 * time.Second

func newClient(t *testing.T, srv *mcpclienttest.Server, opts ...mcpclient.ClientOption) *mcpclient.Client {
	t.Helper()
	return mcpclient.NewClient("shop", srv.URL, mcpclient.NoAuth{}, testTimeout, opts...)
}

func tools(names ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, n := range names {
		out = append(out, mcpclienttest.Tool(n, "does "+n, "", true))
	}
	return out
}

func lastOf(srv *mcpclienttest.Server, method string) mcpclienttest.Request {
	var last mcpclienttest.Request
	for _, r := range srv.Requests() {
		if r.Method == method {
			last = r
		}
	}
	return last
}

// The handshake, a list and a call over plain JSON answers, with no session
// id: none is sent, the protocol header with it.
func TestClientHandshakeListAndCall(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("search", "shop_buy")})
	c := newClient(t, srv, mcpclient.WithVersion("v1.2.3"))
	ctx := context.Background()
	l, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tools) != 2 || l.Tools[0].Name != "shop.buy" || l.Fingerprint == "" {
		t.Fatalf("listed %+v", l)
	}
	res, err := c.CallTool(ctx, "search", json.RawMessage(`{"q":"x"}`))
	if err != nil || res.Text != "ok" || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	var seq []string
	for _, r := range srv.Requests() {
		seq = append(seq, r.Method)
	}
	if got := strings.Join(seq, ","); got != "initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatalf("requests = %s", got)
	}
	init := lastOf(srv, "initialize")
	var p struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ClientInfo      map[string]any `json:"clientInfo"`
	}
	if err := json.Unmarshal(init.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != "2025-11-25" || len(p.Capabilities) != 0 || p.ClientInfo["name"] != "ganglion" || p.ClientInfo["version"] != "v1.2.3" {
		t.Fatalf("initialize params = %s", init.Params)
	}
	call := lastOf(srv, "tools/call")
	if call.Header.Get("Mcp-Session-Id") != "" {
		t.Errorf("a stateless server got a session id: %v", call.Header)
	}
	if call.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
		t.Errorf("a request after initialize lacks the agreed revision: %v", call.Header)
	}
	if lastOf(srv, "initialize").Header.Get("MCP-Protocol-Version") != "" {
		t.Errorf("initialize carried a revision before one was agreed")
	}
	if call.Header.Get("Content-Type") != "application/json" || call.Header.Get("Accept") != "application/json, text/event-stream" {
		t.Errorf("headers = %v", call.Header)
	}
	var cp struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(call.Params, &cp)
	if cp.Name != "search" || string(cp.Arguments) != `{"q":"x"}` {
		t.Fatalf("call params = %s", call.Params)
	}
}

func TestClientSendsNoArgumentsAsAnEmptyObject(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("search")})
	c := newClient(t, srv)
	for _, args := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		if _, err := c.CallTool(context.Background(), "search", args); err != nil {
			t.Fatal(err)
		}
		var p struct {
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(lastOf(srv, "tools/call").Params, &p)
		if string(p.Arguments) != `{}` {
			t.Fatalf("arguments = %s", p.Arguments)
		}
	}
	before := srv.Count("tools/call")
	for _, bad := range []string{`[1]`, `"x"`, `{"a":`} {
		if _, err := c.CallTool(context.Background(), "search", json.RawMessage(bad)); !errors.Is(err, mcpclient.ErrBadArguments) {
			t.Errorf("arguments %s: %v, want ErrBadArguments", bad, err)
		}
	}
	if srv.Count("tools/call") != before {
		t.Fatal("a call with bad arguments was sent")
	}
}

// A session id is kept and sent, with the protocol revision, on every later
// request; a dropped session is replaced once and the request repeated.
func TestClientSessionAndExpiry(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("search"), SessionID: "s"})
	c := newClient(t, srv)
	ctx := context.Background()
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatal(err)
	}
	list := lastOf(srv, "tools/list")
	if list.Header.Get("Mcp-Session-Id") != "s-1" || list.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
		t.Fatalf("list headers = %v", list.Header)
	}
	if lastOf(srv, "notifications/initialized").Header.Get("Mcp-Session-Id") != "s-1" {
		t.Error("the initialized notification did not carry the session")
	}
	srv.ExpireSessions()
	res, err := c.CallTool(ctx, "search", nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("call after expiry = %+v, %v", res, err)
	}
	if srv.Count("initialize") != 2 {
		t.Fatalf("%d initializes, want a new session", srv.Count("initialize"))
	}
	if got := lastOf(srv, "tools/call").Header.Get("Mcp-Session-Id"); got != "s-2" {
		t.Fatalf("the repeated call carried session %q", got)
	}
}

// A second 404 in a row is an error: the client repeats once, not forever.
func TestClientRepeatsAfterExpiryOnce(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("search"), SessionID: "s",
		Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
			if req.Method == "tools/call" {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			return false
		}})
	c := newClient(t, srv)
	if _, err := c.CallTool(context.Background(), "search", nil); !errors.Is(err, mcpclient.ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	if n := srv.Count("tools/call"); n != 2 {
		t.Fatalf("%d calls reached the server, want 2", n)
	}
}

func TestClientRefusesABadSessionID(t *testing.T) {
	for name, id := range map[string]string{"too long": strings.Repeat("a", 257), "a space": "a b", "non-ASCII": "é"} {
		t.Run(name, func(t *testing.T) {
			srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a"),
				Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
					if req.Method != "initialize" {
						return false
					}
					w.Header().Set("Mcp-Session-Id", id)
					mcpclienttest.Answer(w, mcpclienttest.Options{}, req.ID, map[string]any{"protocolVersion": "2025-11-25"})
					return true
				}})
			if err := newClient(t, srv).Initialize(context.Background()); !errors.Is(err, mcpclient.ErrProtocol) {
				t.Fatalf("err = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestClientProtocolRevisions(t *testing.T) {
	for v, ok := range map[string]bool{"2025-11-25": true, "2025-06-18": true, "2024-11-05": true, "1999-01-01": false, "": false} {
		srv := mcpclienttest.New(t, mcpclienttest.Options{ProtocolVersion: v, Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
			if req.Method != "initialize" {
				return false
			}
			mcpclienttest.Answer(w, mcpclienttest.Options{}, req.ID, map[string]any{"protocolVersion": v})
			return true
		}})
		err := newClient(t, srv).Initialize(context.Background())
		if ok != (err == nil) {
			t.Errorf("revision %q: err = %v, accepted = %v", v, err, ok)
		}
	}
}

// A request after initialize carries the revision the server agreed on,
// not the one the client asked for.
func TestClientSendsTheAgreedRevision(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{ProtocolVersion: "2025-06-18"})
	c := newClient(t, srv)
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lastOf(srv, "tools/list").Header.Get("MCP-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("revision sent = %q, want the agreed 2025-06-18", got)
	}
}

// An answer as an event stream: notifications before it are handled, a
// list change reaches the hook, a request of the server is refused with
// "method not supported" and a ping is answered, and none is acted on.
func TestClientEventStream(t *testing.T) {
	pre := []string{
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","id":"s1","method":"sampling/createMessage","params":{"messages":[]}}`,
		`{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{}}`,
		`{"jsonrpc":"2.0","id":"r1","method":"roots/list"}`,
		`{"jsonrpc":"2.0","id":"p1","method":"ping"}`,
		`not json at all`,
		`{"jsonrpc":"2.0","id":99,"result":{}}`,
	}
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("search"), Preamble: pre})
	changed := 0
	c := newClient(t, srv, mcpclient.WithListChanged(func() { changed++ }))
	if _, err := c.CallTool(context.Background(), "search", nil); err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		t.Error("the list change was not passed on")
	}
	replies := map[string]string{}
	for _, r := range srv.Requests() {
		if r.HTTPMethod == http.MethodPost && r.Method == "" {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(r.Body, &m)
			if m.Error != nil {
				replies[string(m.ID)] = fmt.Sprint(m.Error.Code)
			} else {
				replies[string(m.ID)] = string(m.Result)
			}
		}
	}
	want := map[string]string{`"s1"`: "-32601", "7": "-32601", `"r1"`: "-32601", `"p1"`: "{}"}
	for id, w := range want {
		if replies[id] != w {
			t.Errorf("reply to %s = %q, want %q (all: %v)", id, replies[id], w, replies)
		}
	}
	if c.Ignored() < 2 {
		t.Errorf("ignored = %d: a message that is not JSON-RPC and an answer to nothing are counted", c.Ignored())
	}
}

// A server that floods requests is answered only so far.
func TestClientAnswersAFloodOfRequestsUpToALimit(t *testing.T) {
	var pre []string
	for i := range 100 {
		pre = append(pre, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"sampling/createMessage"}`, i+1000))
	}
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a"), Preamble: pre})
	if _, err := newClient(t, srv).CallTool(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == "" && r.HTTPMethod == http.MethodPost {
			n++
		}
	}
	if n > 32 { // 16 for each of the two streams, the handshake's and the call's
		t.Fatalf("%d replies to a flood of 200 requests", n)
	}
}

func TestClientPagesTheList(t *testing.T) {
	var names []string
	for i := range 7 {
		names = append(names, fmt.Sprintf("t%d", i))
	}
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools(names...), PageSize: 3})
	l, err := newClient(t, srv).ListTools(context.Background())
	if err != nil || len(l.Tools) != 7 || srv.Count("tools/list") != 3 {
		t.Fatalf("listed %d tools in %d pages, %v", len(l.Tools), srv.Count("tools/list"), err)
	}
}

// A cursor that repeats, too many pages and a list over 512 KiB are
// errors, and nothing is taken.
func TestClientRefusesAHostileList(t *testing.T) {
	loop := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/list" {
			return false
		}
		mcpclienttest.Answer(w, mcpclienttest.Options{}, req.ID, map[string]any{"tools": []any{}, "nextCursor": "same"})
		return true
	}})
	if _, err := newClient(t, loop).ListTools(context.Background()); !errors.Is(err, mcpclient.ErrProtocol) {
		t.Errorf("a cursor loop: %v", err)
	}
	if n := loop.Count("tools/list"); n > 3 {
		t.Errorf("%d pages requested before the loop was seen", n)
	}

	page := 0
	endless := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/list" {
			return false
		}
		page++
		mcpclienttest.Answer(w, mcpclienttest.Options{}, req.ID, map[string]any{"tools": []any{}, "nextCursor": fmt.Sprint("c", page)})
		return true
	}})
	if _, err := newClient(t, endless).ListTools(context.Background()); !errors.Is(err, mcpclient.ErrProtocol) || page > mcpclient.MaxPages {
		t.Errorf("endless pages: %v after %d pages", err, page)
	}

	big := strings.Repeat("d", 20<<10)
	var many []json.RawMessage
	for i := range 30 {
		many = append(many, mcpclienttest.Tool(fmt.Sprintf("t%d", i), big, "", true))
	}
	over := mcpclienttest.New(t, mcpclienttest.Options{Tools: many, PageSize: 5})
	if _, err := newClient(t, over).ListTools(context.Background()); !errors.Is(err, mcpclient.ErrTooLarge) {
		t.Errorf("a list over 512 KiB across pages: %v", err)
	}
}

func TestClientStatuses(t *testing.T) {
	tests := []struct {
		code int
		is   error
	}{
		{http.StatusUnauthorized, mcpclient.ErrUnauthorized},
		{http.StatusForbidden, mcpclient.ErrForbidden},
	}
	for _, tt := range tests {
		srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, _ mcpclienttest.Request) bool {
			w.WriteHeader(tt.code)
			return true
		}})
		if err := newClient(t, srv).Initialize(context.Background()); !errors.Is(err, tt.is) {
			t.Errorf("status %d: %v, want %v", tt.code, err, tt.is)
		}
	}
	srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, _ mcpclienttest.Request) bool {
		http.Error(w, "secret-body-text", http.StatusInternalServerError)
		return true
	}})
	err := newClient(t, srv).Initialize(context.Background())
	var se *mcpclient.StatusError
	if !errors.As(err, &se) || se.Code != 500 || strings.Contains(err.Error(), "secret-body-text") {
		t.Fatalf("status 500: %v", err)
	}
}

func TestClientRPCErrorIsTyped(t *testing.T) {
	long := strings.Repeat("m", 2000)
	srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/call" {
			return false
		}
		mcpclienttest.AnswerError(w, mcpclienttest.Options{}, req.ID, -32602, long)
		return true
	}})
	_, err := newClient(t, srv).CallTool(context.Background(), "x", nil)
	var rpc *mcpclient.RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32602 || len([]rune(rpc.Message)) > 513 {
		t.Fatalf("err = %v, %+v", err, rpc)
	}
}

// Redirects: the same origin keeps the credential; a message (a POST)
// never follows a redirect to another origin, which would carry the
// call's body there; a GET that does gets neither the credential, the
// session id nor the Referer; https to http is refused, and more than three
// is refused.
func TestClientRedirects(t *testing.T) {
	other := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a")})
	var otherGET http.Header
	otherStream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherGET = r.Header.Clone()
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(otherStream.Close)
	to := func(target string) *mcpclienttest.Server {
		return mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, r *http.Request, _ mcpclienttest.Request) bool {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return true
		}})
	}
	auth := keyAuth{name: "X-Api-Key", value: "sekret-value"}

	away := to(other.URL)
	c := mcpclient.NewClient("shop", away.URL, auth, testTimeout)
	if _, err := c.ListTools(context.Background()); !errors.Is(err, mcpclient.ErrRedirect) {
		t.Fatalf("a POST redirected to another origin: %v, want ErrRedirect", err)
	}
	if n := len(other.Requests()); n != 0 {
		t.Errorf("the other origin got %d messages", n)
	}
	if got := lastOf(away, "initialize"); got.Header.Get("X-Api-Key") != "sekret-value" {
		t.Errorf("the server's own origin did not get it: %v", got.Header)
	}

	// The server's own origin answers the messages and sends its GET stream
	// elsewhere.
	backend := mcpclienttest.New(t, mcpclienttest.Options{SessionID: "s", Tools: tools("a")})
	backendURL, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, otherStream.URL, http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(stream.Close)
	err := mcpclient.NewClient("shop", stream.URL, auth, testTimeout).Listen(context.Background())
	if !errors.Is(err, mcpclient.ErrNoStream) || otherGET == nil {
		t.Fatalf("a GET redirected to another origin: %v, reached it: %v", err, otherGET != nil)
	}
	if otherGET.Get("X-Api-Key") != "" || otherGET.Get("Mcp-Session-Id") != "" || otherGET.Get("Referer") != "" {
		t.Errorf("the other origin got the credential, the session or the Referer: %v", otherGET)
	}

	same := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a")})
	var seen http.Header
	same.Update(func(o *mcpclienttest.Options) {
		o.Intercept = func(w http.ResponseWriter, r *http.Request, req mcpclienttest.Request) bool {
			if r.URL.Path == "/" && req.Method == "initialize" {
				http.Redirect(w, r, "/again", http.StatusTemporaryRedirect)
				return true
			}
			if r.URL.Path == "/again" {
				seen = r.Header.Clone()
			}
			return false
		}
	})
	if err := mcpclient.NewClient("shop", same.URL, auth, testTimeout).Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen.Get("X-Api-Key") != "sekret-value" {
		t.Errorf("a same-origin redirect lost the credential: %v", seen)
	}

	loop := mcpclienttest.New(t, mcpclienttest.Options{})
	loop.Update(func(o *mcpclienttest.Options) {
		o.Intercept = func(w http.ResponseWriter, r *http.Request, _ mcpclienttest.Request) bool {
			http.Redirect(w, r, loop.URL+"/x", http.StatusTemporaryRedirect)
			return true
		}
	})
	err = mcpclient.NewClient("shop", loop.URL, auth, testTimeout).Initialize(context.Background())
	if !errors.Is(err, mcpclient.ErrRedirect) || strings.Contains(err.Error(), "sekret-value") || strings.Contains(err.Error(), loop.URL) {
		t.Errorf("a redirect loop: %v", err)
	}
}

func TestClientRefusesAnHTTPSToHTTPRedirect(t *testing.T) {
	plain := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a")})
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(tlsSrv.Close)
	c := mcpclient.NewClient("shop", tlsSrv.URL, mcpclient.NoAuth{}, testTimeout, mcpclient.WithHTTPClient(tlsSrv.Client()))
	if err := c.Initialize(context.Background()); !errors.Is(err, mcpclient.ErrRedirect) {
		t.Fatalf("err = %v, want ErrRedirect", err)
	}
	if plain.Count("initialize") != 0 {
		t.Fatal("the downgraded request reached the plain server")
	}
}

// Hostile sizes: one JSON answer over the limit, an event stream that never
// ends, a line over the limit and an event over the limit are ErrTooLarge,
// and the client reads no more than its limit of them.
func TestClientBoundsEveryRead(t *testing.T) {
	huge := strings.Repeat("a", mcpclient.MaxCallBytes+10)
	json1 := mcpclienttest.New(t, mcpclienttest.Options{Call: func(string, json.RawMessage) any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": huge}}}
	}})
	if _, err := newClient(t, json1).CallTool(context.Background(), "a", nil); !errors.Is(err, mcpclient.ErrTooLarge) {
		t.Errorf("a JSON answer over the limit: %v", err)
	}

	var sent atomic.Int64
	endless := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, r *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := []byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
		for r.Context().Err() == nil && sent.Load() < 50<<20 {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				break
			}
		}
		return true
	}})
	_, err := newClient(t, endless).CallTool(context.Background(), "a", nil)
	if !errors.Is(err, mcpclient.ErrTooLarge) || sent.Load() >= 50<<20 {
		t.Errorf("an endless stream: %v after %d bytes", err, sent.Load())
	}

	for name, body := range map[string]string{
		"a line over the limit":   "data: " + strings.Repeat("x", mcpclient.MaxCallBytes+100) + "\n\n",
		"an event over the limit": strings.Repeat("data: "+strings.Repeat("x", 1<<20)+"\n", 5) + "\n",
	} {
		srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
			if req.Method != "tools/call" {
				return false
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(body))
			return true
		}})
		if _, err := newClient(t, srv).CallTool(context.Background(), "a", nil); !errors.Is(err, mcpclient.ErrTooLarge) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestClientStreamEndsWithoutAnAnswer(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, _ *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/call" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n"))
		return true
	}})
	if _, err := newClient(t, srv).CallTool(context.Background(), "a", nil); !errors.Is(err, mcpclient.ErrNoAnswer) {
		t.Fatalf("err = %v, want ErrNoAnswer", err)
	}
}

// A server that hangs ends at the timeout, as unreachable and not as "not
// sent"; the call was made once and never repeated.
func TestClientTimeoutIsNotRepeated(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a"), Intercept: func(_ http.ResponseWriter, r *http.Request, req mcpclienttest.Request) bool {
		if req.Method != "tools/call" {
			return false
		}
		<-r.Context().Done()
		return true
	}})
	c := mcpclient.NewClient("shop", srv.URL, mcpclient.NoAuth{}, 100*time.Millisecond)
	_, err := c.CallTool(context.Background(), "a", nil)
	if !errors.Is(err, mcpclient.ErrUnreachable) || !errors.Is(err, context.DeadlineExceeded) || mcpclient.NotSent(err) {
		t.Fatalf("err = %v", err)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Fatalf("the hanging call reached the server %d times, want once", n)
	}
}

func TestClientNothingListeningIsNotSent(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{})
	url := srv.URL
	srv.Close()
	err := mcpclient.NewClient("shop", url, mcpclient.NoAuth{}, testTimeout).Initialize(context.Background())
	if !errors.Is(err, mcpclient.ErrUnreachable) || !mcpclient.NotSent(err) || strings.Contains(err.Error(), url) {
		t.Fatalf("err = %v (not sent: %v)", err, mcpclient.NotSent(err))
	}
}

func TestClientCancelledByItsContext(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newClient(t, srv).Initialize(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, mcpclient.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientListen(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{Tools: tools("a"), ListenStream: true, SessionID: "s"})
	got := make(chan struct{}, 4)
	c := newClient(t, srv, mcpclient.WithListChanged(func() { got <- struct{}{} }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Listen(ctx) }()
	<-srv.Opened()
	if lastGet(srv).Header.Get("Mcp-Session-Id") != "s-1" || lastGet(srv).Header.Get("Accept") != "text/event-stream" {
		t.Errorf("GET headers = %v", lastGet(srv).Header)
	}
	srv.NotifyListChanged()
	<-got
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled listen returned nil")
	}
}

func lastGet(srv *mcpclienttest.Server) mcpclienttest.Request {
	var last mcpclienttest.Request
	for _, r := range srv.Requests() {
		if r.HTTPMethod == http.MethodGet {
			last = r
		}
	}
	return last
}

func TestClientListenWithoutAStream(t *testing.T) {
	srv := mcpclienttest.New(t, mcpclienttest.Options{})
	if err := newClient(t, srv).Listen(context.Background()); !errors.Is(err, mcpclient.ErrNoStream) {
		t.Fatalf("err = %v, want ErrNoStream", err)
	}
}

// keyAuth sends a header with a secret value.
type keyAuth struct{ name, value string }

func (k keyAuth) Header(context.Context) (string, string, error) { return k.name, k.value, nil }

func (keyAuth) Rejected(context.Context, string) {}

type failingAuth struct{ err error }

func (f failingAuth) Header(context.Context) (string, string, error) { return "", "", f.err }

func (failingAuth) Rejected(context.Context, string) {}

// No error and no log line holds the credential or the address's query,
// whatever failed.
func TestClientNeverLeaksTheCredential(t *testing.T) {
	const secret = "sekret-value-123"
	auth := keyAuth{name: "X-Api-Key", value: secret}
	var logs bytes.Buffer
	_ = slog.New(slog.NewTextHandler(&logs, nil))
	cases := map[string]func() error{
		"401": func() error {
			srv := mcpclienttest.New(t, mcpclienttest.Options{Header: "X-Other", Value: "v"})
			return mcpclient.NewClient("shop", srv.URL+"/mcp?key=urlsecret", auth, testTimeout).Initialize(context.Background())
		},
		"500": func() error {
			srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, r *http.Request, _ mcpclienttest.Request) bool {
				http.Error(w, "echo "+r.Header.Get("X-Api-Key"), 500)
				return true
			}})
			return mcpclient.NewClient("shop", srv.URL+"?key=urlsecret", auth, testTimeout).Initialize(context.Background())
		},
		"timeout": func() error {
			srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(_ http.ResponseWriter, r *http.Request, _ mcpclienttest.Request) bool {
				<-r.Context().Done()
				return true
			}})
			return mcpclient.NewClient("shop", srv.URL+"?key=urlsecret", auth, 50*time.Millisecond).Initialize(context.Background())
		},
		"redirect": func() error {
			other := mcpclienttest.New(t, mcpclienttest.Options{})
			srv := mcpclienttest.New(t, mcpclienttest.Options{Intercept: func(w http.ResponseWriter, r *http.Request, _ mcpclienttest.Request) bool {
				http.Redirect(w, r, other.URL+"?key=urlsecret", http.StatusFound)
				return true
			}})
			other.Update(func(o *mcpclienttest.Options) { o.Header, o.Value = "X-Api-Key", "never" })
			return mcpclient.NewClient("shop", srv.URL, auth, testTimeout).Initialize(context.Background())
		},
		"dial": func() error {
			srv := mcpclienttest.New(t, mcpclienttest.Options{})
			u := srv.URL
			srv.Close()
			return mcpclient.NewClient("shop", u+"?key=urlsecret", auth, testTimeout).Initialize(context.Background())
		},
		"auth fails": func() error {
			srv := mcpclienttest.New(t, mcpclienttest.Options{})
			return mcpclient.NewClient("shop", srv.URL, failingAuth{errors.New("vault says " + secret)}, testTimeout).Initialize(context.Background())
		},
	}
	for name, run := range cases {
		err := run()
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, bad := range []string{secret, "urlsecret", "127.0.0.1", "?key"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: the error %q holds %q", name, err, bad)
			}
		}
	}
	if err := cases["auth fails"](); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Errorf("an Auth that fails: %v, want ErrUnauthorized", err)
	}
	if strings.Contains(logs.String(), secret) {
		t.Error("the log holds the credential")
	}
}

// initialize keeps the server's instructions for the session, cut to the
// limit; none, or a value that is not a string, is none (R-MCP-11).
func TestInitializeKeepsInstructions(t *testing.T) {
	long := strings.Repeat("я", mcpclient.MaxInstructionsRunes+50)
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"given", "Filter by month.", "Filter by month."},
		{"too long", long, strings.Repeat("я", mcpclient.MaxInstructionsRunes)},
		{"absent", nil, ""},
		{"a number", 7, ""},
		{"an object", map[string]any{"a": "b"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, mcpclienttest.New(t, mcpclienttest.Options{Instructions: tt.in}))
			if got := c.Instructions(); got != "" {
				t.Fatalf("before initialize: %q", got)
			}
			if err := c.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := c.Instructions(); got != tt.want {
				t.Errorf("Instructions = %q, want %q", got, tt.want)
			}
		})
	}
}
