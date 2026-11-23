package modhttp_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modhttp"
	"github.com/igorrius/ganglion/pdk"
)

// fake is the module host: the module "demo" answers GET / and POST /api
// with what answer says, and every request it was asked is kept.
type fake struct {
	mu     sync.Mutex
	asked  []pdk.HTTPRequest
	answer pdk.HTTPAnswer
	err    error
	block  chan struct{} // when set, a request waits on it
}

func (f *fake) Pages() []modhttp.Page {
	return []modhttp.Page{{Short: "demo", Routes: []string{"GET /", "POST /api"}}, {Short: "x<script>", Routes: []string{"GET /"}}}
}

func (f *fake) ServeHTTP(_ context.Context, short string, req pdk.HTTPRequest) (pdk.HTTPAnswer, error) {
	f.mu.Lock()
	f.asked = append(f.asked, req)
	block, ans, err := f.block, f.answer, f.err
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if short != "demo" {
		return pdk.HTTPAnswer{}, modhttp.ErrNoPage
	}
	return ans, err
}

func (f *fake) requests() []pdk.HTTPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// serve starts a listener for f on 127.0.0.1 and returns its address.
func serve(t *testing.T, f *fake) string {
	t.Helper()
	l, err := modhttp.New(modhttp.DefaultConfig("127.0.0.1:0"), f, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-l.Bound():
	case err := <-done:
		t.Fatal(err)
	}
	return l.Addr()
}

// raw sends req as it is, bytes and all, and reads the answer: a browser
// and Go's client both clean what these tests need to send dirty.
func raw(t *testing.T, addr, req string) *http.Response {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// The listener may answer and close before the request is all sent (a
	// body over the cap): the write goes on its own, and its error is the
	// listener's refusal, not the test's.
	go func() { _, _ = io.WriteString(c, req) }()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// get is a GET of path with the Host and the extra header lines given.
func get(t *testing.T, addr, path, host string, extra ...string) *http.Response {
	t.Helper()
	return raw(t, addr, "GET "+path+" HTTP/1.1\r\nHost: "+host+"\r\n"+strings.Join(extra, "")+"Connection: close\r\n\r\n")
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ok() pdk.HTTPAnswer {
	return pdk.HTTPAnswer{Status: 200, Headers: map[string]string{"content-type": "text/html; charset=utf-8"}, Body: []byte("<p>hi</p>")}
}

// own is the Host of the listener at addr, as a browser sends it.
func own(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return "127.0.0.1:" + port
}

// wantSecurityHeaders checks the node's headers on an answer, whatever its
// status.
func wantSecurityHeaders(t *testing.T, r *http.Response) {
	t.Helper()
	for name, want := range map[string]string{
		"Content-Security-Policy":      "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
	} {
		if got := r.Header.Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want exactly %q", name, got, want)
		}
	}
}

func TestAPageAnswersWithTheModulesAnswerAndTheNodesHeaders(t *testing.T) {
	f := &fake{answer: ok()}
	addr := serve(t, f)
	r := get(t, addr, "/demo/?n=1&x=%20y", own(addr), "Accept: text/html\r\n", "Cookie: session=1\r\n", "Authorization: Basic eDp5\r\n", "If-None-Match: \"v1\"\r\n")
	if r.StatusCode != 200 || body(t, r) != "<p>hi</p>" || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status %d, content-type %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
	wantSecurityHeaders(t, r)
	got := f.requests()
	if len(got) != 1 {
		t.Fatalf("asked %d times", len(got))
	}
	want := map[string]string{"accept": "text/html", "if-none-match": `"v1"`}
	if q := got[0]; q.Method != "GET" || q.Path != "/" || q.Query != "n=1&x=%20y" || fmt.Sprint(q.Headers) != fmt.Sprint(want) || len(q.Body) != 0 {
		t.Fatalf("the module was asked %+v", q)
	}
}

func TestTheListenerRefusesAForeignHost(t *testing.T) {
	f := &fake{answer: ok()}
	addr := serve(t, f)
	_, port, _ := net.SplitHostPort(addr)
	for _, host := range []string{
		"evil.example.com:" + port, // DNS rebinding: a foreign name that resolves to loopback
		"evil.example.com",
		"127.0.0.1",          // the port is part of the address
		"127.0.0.1:1" + port, // another port
		"localhost.:" + port, // a trailing dot is another name
		"127.0.0.2:" + port,  // another loopback address
		"0.0.0.0:" + port,    // the unspecified address
	} {
		r := get(t, addr, "/demo/", host)
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, r.StatusCode)
		}
		wantSecurityHeaders(t, r)
	}
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "LocalHost:" + port} {
		if r := get(t, addr, "/demo/", host); r.StatusCode != 200 {
			t.Errorf("Host %q: status %d, want 200", host, r.StatusCode)
		}
	}
	// A request line in absolute form names the host the server goes by:
	// a foreign one is refused even with the right Host header.
	r := raw(t, addr, "GET http://evil.example.com:"+port+"/demo/ HTTP/1.1\r\nHost: 127.0.0.1:"+port+"\r\nConnection: close\r\n\r\n")
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("absolute form with a foreign host: status %d, want 403", r.StatusCode)
	}
	// Junk in the Host is refused before the listener sees it.
	if r := get(t, addr, "/demo/", "127.0.0.1:"+port+"@evil.example.com"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("junk after the port: status %d, want 400", r.StatusCode)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("the module was asked %d times, want only for the 3 own hosts", n)
	}
}

func TestTheListenerRefusesAnotherSite(t *testing.T) {
	f := &fake{answer: ok()}
	addr := serve(t, f)
	host := own(addr)
	post := func(extra string) *http.Response {
		return raw(t, addr, "POST /demo/api HTTP/1.1\r\nHost: "+host+"\r\nContent-Length: 2\r\n"+extra+"Connection: close\r\n\r\n{}")
	}
	for name, r := range map[string]*http.Response{
		"a foreign Origin":            post("Origin: https://evil.example.com\r\n"),
		"the null Origin":             post("Origin: null\r\n"),
		"https on the same host":      post("Origin: https://" + host + "\r\n"),
		"a write from another site":   post("Sec-Fetch-Site: cross-site\r\n"),
		"a write from a sibling":      post("Sec-Fetch-Site: same-site\r\n"),
		"a script of another site":    get(t, addr, "/demo/", host, "Sec-Fetch-Site: cross-site\r\n", "Sec-Fetch-Mode: no-cors\r\n", "Sec-Fetch-Dest: script\r\n"),
		"a fetch of another site":     get(t, addr, "/demo/", host, "Sec-Fetch-Site: cross-site\r\n", "Sec-Fetch-Mode: cors\r\n"),
		"a GET with a foreign Origin": get(t, addr, "/demo/", host, "Origin: http://evil.example.com\r\n"),
	} {
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, r.StatusCode)
		}
		if r.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: a CORS header was sent", name)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("the module was asked %d times", n)
	}
	for name, r := range map[string]*http.Response{
		"the page's own POST":                   post("Origin: http://" + host + "\r\n" + "Sec-Fetch-Site: same-origin\r\n"),
		"the owner following a link":            get(t, addr, "/demo/", host, "Sec-Fetch-Site: cross-site\r\n", "Sec-Fetch-Mode: navigate\r\n"),
		"the owner typing the address":          get(t, addr, "/demo/", host, "Sec-Fetch-Site: none\r\n", "Sec-Fetch-Mode: navigate\r\n"),
		"a client that sends no fetch metadata": get(t, addr, "/demo/", host),
	} {
		if r.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", name, r.StatusCode)
		}
	}
}

func TestTheListenerRefusesAPathOutOfThePrefix(t *testing.T) {
	f := &fake{answer: ok()}
	addr := serve(t, f)
	host := own(addr)
	for _, path := range []string{
		"/demo/../other/",               // a dot segment, as sent
		"/demo/%2e%2e/other/",           // encoded dots
		"/demo/%2E%2E/other/",           // in upper case
		"/demo/..%2fother/",             // an encoded slash
		"/demo/..%2Fother/",             // in upper case
		"/demo/..%5cother/",             // an encoded backslash
		"/demo/..\\x/",                  // a backslash
		"/demo//api",                    // an empty segment
		"/demo/./api",                   // a dot segment
		"/demo/%00",                     // a NUL byte
		"/demo/%0d%0aSet-Cookie:%20a=b", // a line break
	} {
		r := get(t, addr, path, host)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", path, r.StatusCode)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("the module was asked %d times: %+v", n, f.requests())
	}
	// Another module's prefix is another module: the fake's only page is
	// demo, the rest are not found.
	if r := get(t, addr, "/other/", host); r.StatusCode != http.StatusNotFound {
		t.Errorf("another prefix: status %d", r.StatusCode)
	}
}

func TestTheListenerCapsTheBody(t *testing.T) {
	f := &fake{answer: ok()}
	addr := serve(t, f)
	host := own(addr)
	big := strings.Repeat("a", modhttp.MaxBody+1)
	r := raw(t, addr, fmt.Sprintf("POST /demo/api HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", host, len(big), big))
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a long body: status %d, want 413", r.StatusCode)
	}
	// Chunked, so no length warns the listener: it stops reading at the cap.
	chunked := fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(big), big)
	r = raw(t, addr, "POST /demo/api HTTP/1.1\r\nHost: "+host+"\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n"+chunked)
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a long chunked body: status %d, want 413", r.StatusCode)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("the module was asked %d times", n)
	}
	exact := strings.Repeat("b", modhttp.MaxBody)
	r = raw(t, addr, fmt.Sprintf("POST /demo/api HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s", host, len(exact), exact))
	if r.StatusCode != 200 {
		t.Fatalf("a body of exactly 1 MiB: status %d", r.StatusCode)
	}
	if got := f.requests(); len(got) != 1 || len(got[0].Body) != modhttp.MaxBody || got[0].Headers["content-type"] != "text/plain" {
		t.Fatalf("the module was asked %d times", len(got))
	}
}

func TestTheNodeDropsTheModulesOwnHeaders(t *testing.T) {
	f := &fake{answer: pdk.HTTPAnswer{Status: 201, Body: []byte("x"), Headers: map[string]string{
		"Set-Cookie":                       "session=stolen",
		"set-cookie":                       "other=1",
		"Content-Security-Policy":          "script-src *",
		"X-Content-Type-Options":           "",
		"Access-Control-Allow-Origin":      "*",
		"Access-Control-Allow-Credentials": "true",
		"Location":                         "https://evil.example.com/",
		"Refresh":                          "0;url=https://evil.example.com/",
		"ETag":                             "\"a\"\r\nSet-Cookie: c=d",
		"Cache-Control":                    "max-age=60",
		"CONTENT-TYPE":                     "application/json",
	}}}
	addr := serve(t, f)
	r := get(t, addr, "/demo/", own(addr))
	if r.StatusCode != 201 || body(t, r) != "x" {
		t.Fatalf("status %d", r.StatusCode)
	}
	wantSecurityHeaders(t, r)
	for _, name := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Location", "Refresh", "ETag"} {
		if v := r.Header.Values(name); len(v) > 0 {
			t.Errorf("%s reached the browser: %q", name, v)
		}
	}
	if r.Header.Get("Cache-Control") != "max-age=60" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("the allowed headers: cache-control %q, content-type %q", r.Header.Get("Cache-Control"), r.Header.Get("Content-Type"))
	}
}

func TestAnAnswerWithNoContentTypeIsNotSniffed(t *testing.T) {
	f := &fake{answer: pdk.HTTPAnswer{Status: 200, Body: []byte("<script>alert(1)</script>")}}
	addr := serve(t, f)
	r := get(t, addr, "/demo/", own(addr))
	if r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("content-type %q", r.Header.Get("Content-Type"))
	}
}

func TestTheModuleHostsRefusalsHaveTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		header [2]string
	}{
		{modhttp.ErrNoPage, http.StatusNotFound, [2]string{}},
		{&modhttp.MethodError{Allow: []string{"GET", "POST"}}, http.StatusMethodNotAllowed, [2]string{"Allow", "GET, POST"}},
		{modhttp.ErrBusy, http.StatusTooManyRequests, [2]string{"Retry-After", "1"}},
		{modhttp.ErrTimedOut, http.StatusGatewayTimeout, [2]string{}},
		{modhttp.ErrFailed, http.StatusBadGateway, [2]string{}},
		{fmt.Errorf("modhost: %w", io.ErrUnexpectedEOF), http.StatusInternalServerError, [2]string{}},
	} {
		f := &fake{answer: ok(), err: tc.err}
		addr := serve(t, f)
		r := get(t, addr, "/demo/", own(addr))
		b := body(t, r)
		if r.StatusCode != tc.status {
			t.Errorf("%v: status %d, want %d", tc.err, r.StatusCode, tc.status)
		}
		if tc.header[0] != "" && r.Header.Get(tc.header[0]) != tc.header[1] {
			t.Errorf("%v: %s = %q", tc.err, tc.header[0], r.Header.Get(tc.header[0]))
		}
		if strings.Contains(b, "<p>hi</p>") || strings.Contains(b, "unexpected EOF") {
			t.Errorf("%v: the body %q is not the node's words", tc.err, b)
		}
		wantSecurityHeaders(t, r)
	}
}

func TestTheListenerRefusesWhatItCannotTakeAtOnce(t *testing.T) {
	f := &fake{answer: ok(), block: make(chan struct{})}
	addr := serve(t, f)
	host := own(addr)
	const held = 32
	var wg sync.WaitGroup
	codes := make(chan int, held)
	for range held {
		wg.Go(func() {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = c.Close() }()
			_, _ = io.WriteString(c, "GET /demo/ HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
			r, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Error(err)
				return
			}
			codes <- r.StatusCode
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.requests()) < held && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(f.requests()); n != held {
		t.Fatalf("%d requests reached the module, want %d", n, held)
	}
	r := get(t, addr, "/demo/", host)
	if r.StatusCode != http.StatusServiceUnavailable || r.Header.Get("Retry-After") != "1" {
		t.Errorf("one more: status %d, Retry-After %q", r.StatusCode, r.Header.Get("Retry-After"))
	}
	close(f.block)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Errorf("a held request ended %d", c)
		}
	}
}

func TestTheIndexListsThePagesEscaped(t *testing.T) {
	f := &fake{}
	addr := serve(t, f)
	r := get(t, addr, "/", own(addr))
	b := body(t, r)
	if r.StatusCode != 200 || !strings.Contains(b, `<a href="/demo/">demo</a>`) || strings.Contains(b, "<script>") {
		t.Fatalf("status %d, body %s", r.StatusCode, b)
	}
	wantSecurityHeaders(t, r)
	r = raw(t, addr, "POST / HTTP/1.1\r\nHost: "+own(addr)+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	if r.StatusCode != http.StatusMethodNotAllowed || r.Header.Get("Allow") != "GET" {
		t.Fatalf("POST /: status %d", r.StatusCode)
	}
}

func TestAModulesNameWithoutTheSlashLeadsToItsPage(t *testing.T) {
	f := &fake{}
	addr := serve(t, f)
	r := get(t, addr, "/demo", own(addr))
	if r.StatusCode != http.StatusPermanentRedirect || r.Header.Get("Location") != "/demo/" {
		t.Fatalf("status %d, Location %q", r.StatusCode, r.Header.Get("Location"))
	}
	if r := get(t, addr, "/nobody", own(addr)); r.StatusCode != http.StatusNotFound || r.Header.Get("Location") != "" {
		t.Fatalf("an unknown name: status %d", r.StatusCode)
	}
}

func TestTheListenerBindsLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8090", "[::]:8090", "192.168.1.10:8090", "example.com:8090", ":8090", "127.0.0.1", "[fe80::1%eth0]:8090", "[::ffff:10.0.0.1]:8090"} {
		if _, err := modhttp.New(modhttp.DefaultConfig(addr), &fake{}, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "modhttp: address") {
			t.Errorf("%q: %v, want a refusal", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0", "LOCALHOST:0", "[::ffff:127.0.0.1]:0"} {
		l, err := modhttp.New(modhttp.DefaultConfig(addr), &fake{}, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Errorf("%q: %v", addr, err)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		select {
		case <-l.Bound():
			host, _, _ := net.SplitHostPort(l.Addr())
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				t.Errorf("%q bound %s", addr, l.Addr())
			}
			cancel()
			if err := <-done; err != nil {
				t.Errorf("%q: %v", addr, err)
			}
		case err := <-done:
			cancel()
			if !strings.Contains(addr, "::1") { // a host with no IPv6 cannot bind ::1
				t.Errorf("%q: %v", addr, err)
			}
		}
	}
}
