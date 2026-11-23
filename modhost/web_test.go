package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modhttp"
	"github.com/igorrius/ganglion/pdk"
)

// pageManifest is the probe as a page module: GET / and POST /api, the
// answer scripted by op_http in its state.
const pageManifest = `name: probe
version: 1
triggers:
  - http:
      routes:
        - {path: /, methods: [GET]}
        - {path: /api, methods: [GET, POST]}
capabilities:
  state: true
limits:
  timeout: 2s
`

// pageEnv is a host running the page module local/page.
func pageEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, active(t, "local/page", pageManifest), active(t, "local/plain", probeManifest))
	e.real = true
	e.start(t)
	return e
}

// script sets what the page answers to its next requests.
func (e *env) script(answer any) {
	b, _ := json.Marshal(map[string]any{"op": "answer", "json": answer})
	e.state.Put("page", "op_http", string(b))
}

func TestAPageModuleIsGivenTheRequestAndAnswers(t *testing.T) {
	e := pageEnv(t)
	e.script(map[string]any{"status": 200, "headers": map[string]string{"content-type": "text/html"}, "body": []byte("<p>ok</p>")})
	req := pdk.HTTPRequest{Method: "POST", Path: "/api", Query: "a=1", Headers: map[string]string{"content-type": "application/json"}, Body: []byte(`{"x":1}`)}
	ans, err := e.host.ServeHTTP(context.Background(), "page", req)
	if err != nil || ans.Status != 200 || string(ans.Body) != "<p>ok</p>" || ans.Headers["content-type"] != "text/html" {
		t.Fatalf("answer %+v, %v", ans, err)
	}
	got, _ := e.state.Get("page", "last_http")
	var seen pdk.HTTPRequest
	if err := json.Unmarshal([]byte(got), &seen); err != nil || seen.Method != "POST" || seen.Path != "/api" || seen.Query != "a=1" || string(seen.Body) != `{"x":1}` {
		t.Fatalf("the module saw %s", got)
	}
	// The wire shape the template codes against (R-HTTP-2).
	for _, k := range []string{`"method":"POST"`, `"path":"/api"`, `"query":"a=1"`, `"headers":{"content-type":"application/json"}`, `"body":"eyJ4IjoxfQ=="`} {
		if !strings.Contains(got, k) {
			t.Errorf("the request %s has no %s", got, k)
		}
	}
	if pages := e.host.Pages(); len(pages) != 1 || pages[0].Short != "page" || !slices.Equal(pages[0].Routes, []string{"GET /", "GET /api", "POST /api"}) {
		t.Fatalf("pages %+v", pages)
	}
	if n := len(e.store.journal); n != 0 {
		t.Fatalf("a successful page run was journaled: %d rows", n)
	}
}

func TestAPageModuleIsAskedOnlyForItsRoutes(t *testing.T) {
	e := pageEnv(t)
	e.script(map[string]any{"status": 200})
	for _, tc := range []struct {
		short, method, path string
		want                error
	}{
		{"page", "GET", "/missing", modhttp.ErrNoPage},
		{"page", "GET", "/api/", modhttp.ErrNoPage}, // exact paths only
		{"page", "GET", "", modhttp.ErrNoPage},
		{"plain", "GET", "/", modhttp.ErrNoPage}, // a module with no http trigger
		{"nobody", "GET", "/", modhttp.ErrNoPage},
		{"page/../plain", "GET", "/", modhttp.ErrNoPage},
	} {
		if _, err := e.host.ServeHTTP(context.Background(), tc.short, pdk.HTTPRequest{Method: tc.method, Path: tc.path}); !errors.Is(err, tc.want) {
			t.Errorf("%s %s %s: %v, want %v", tc.short, tc.method, tc.path, err, tc.want)
		}
	}
	_, err := e.host.ServeHTTP(context.Background(), "page", pdk.HTTPRequest{Method: "DELETE", Path: "/api"})
	if me, ok := errors.AsType[*modhttp.MethodError](err); !ok || !slices.Equal(me.Allow, []string{"GET", "POST"}) {
		t.Fatalf("DELETE /api: %v", err)
	}
	if _, ok := e.state.Get("page", "last_http"); ok {
		t.Fatal("the module ran for a request it did not declare")
	}
}

func TestAPageModulesBrokenAnswerIsTheNodesFailure(t *testing.T) {
	e := pageEnv(t)
	for name, answer := range map[string]any{
		"a status below 200": map[string]any{"status": 101},
		"a status above 599": map[string]any{"status": 600},
		"an unknown field":   map[string]any{"status": 200, "cookie": "x"},
		"not an object":      "<p>hi</p>",
	} {
		e.script(answer)
		before := len(e.store.counts)
		_, err := e.host.ServeHTTP(context.Background(), "page", pdk.HTTPRequest{Method: "GET", Path: "/"})
		if !errors.Is(err, modhttp.ErrFailed) {
			t.Errorf("%s: %v, want ErrFailed", name, err)
		}
		if c := e.store.counts[before:]; len(c) != 1 || !c[0].r.Failed || c[0].r.Kind != string(modhost.KindFormat) {
			t.Errorf("%s: counted %+v", name, c)
		}
		e.host.Reload(context.Background(), "local/page") //nolint:errcheck // a fresh count of failures: five would turn it off
	}
	if n := len(e.store.journal); n == 0 || e.store.journal[0].Trigger != "http" || e.store.journal[0].Kind != string(modhost.KindFormat) {
		t.Fatalf("a failed page run is not journaled: %+v", e.store.journal)
	}
	for _, line := range e.inbox.events {
		if strings.Contains(string(line.Payload), "cookie") {
			t.Fatalf("a page's failure was escalated with the module's text: %s", line.Payload)
		}
	}
}

func TestAPageModuleThatRunsTooLongTimesOut(t *testing.T) {
	e := pageEnv(t)
	e.state.Put("page", "op_http", `{"op":"spin"}`)
	if _, err := e.host.ServeHTTP(context.Background(), "page", pdk.HTTPRequest{Method: "GET", Path: "/"}); !errors.Is(err, modhttp.ErrTimedOut) {
		t.Fatalf("%v, want ErrTimedOut", err)
	}
}

func TestAPageModuleTakesFourRequestsAtOnce(t *testing.T) {
	e := pageEnv(t)
	e.state.Put("page", "op_http", `{"op":"spin"}`) // each holds the module for its 2s limit
	var wg sync.WaitGroup
	for range modhttp.MaxAtOnce {
		wg.Go(func() {
			_, _ = e.host.ServeHTTP(context.Background(), "page", pdk.HTTPRequest{Method: "GET", Path: "/"})
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.host.WebHeld("page") < modhttp.MaxAtOnce && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := e.host.ServeHTTP(context.Background(), "page", pdk.HTTPRequest{Method: "GET", Path: "/"}); !errors.Is(err, modhttp.ErrBusy) {
		t.Fatalf("the fifth: %v, want ErrBusy", err)
	}
	wg.Wait()
}

// The listener and the host together: what reaches the browser of a real
// module's answer (design section 9, the abuse tests of M11).
func TestAPageReachesTheBrowserOnlyWithTheNodesHeaders(t *testing.T) {
	e := pageEnv(t)
	e.script(map[string]any{"status": 200, "body": []byte("<p>ok</p>"), "headers": map[string]string{
		"Content-Type": "text/html; charset=utf-8", "Set-Cookie": "s=1", "Content-Security-Policy": "script-src *",
		"Access-Control-Allow-Origin": "*",
	}})
	l, err := modhttp.New(modhttp.DefaultConfig("127.0.0.1:0"), e.host, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-l.Bound()
	_, port, _ := net.SplitHostPort(l.Addr())
	do := func(host string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, "http://"+l.Addr()+"/page/", nil)
		req.Host = host
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Body.Close() })
		return r
	}
	r := do("localhost:" + port)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(b) != "<p>ok</p>" || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status %d, body %q", r.StatusCode, b)
	}
	if r.Header.Get("Set-Cookie") != "" || r.Header.Get("Access-Control-Allow-Origin") != "" ||
		r.Header.Get("Content-Security-Policy") != "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'" ||
		r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", r.Header)
	}
	if r := do("rebind.example.com:" + port); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign Host: status %d, want 403", r.StatusCode)
	}
}
