package wsproxy_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/bus/wsproxy"
)

// What is not a websocket upgrade on one of the two paths never reaches
// the server, and what the client set about itself is dropped.
func TestProxyAdmitsOnlyUpgradesOnItsPathsAndDropsIdentityHeaders(t *testing.T) {
	seen := make(chan *http.Request, 4)
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		http.Error(w, "backend", http.StatusTeapot)
	}))
	defer back.Close()
	h, err := wsproxy.New(strings.TrimPrefix(back.URL, "http://"), wsproxy.DefaultConfig(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, hdr map[string]string) int {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	up := map[string]string{"Connection": "keep-alive, Upgrade", "Upgrade": "websocket"}
	for name, c := range map[string]struct {
		method, path string
		hdr          map[string]string
		want         int
	}{
		"no upgrade":        {"GET", "/bus", nil, 400},
		"wrong upgrade":     {"GET", "/bus", map[string]string{"Connection": "Upgrade", "Upgrade": "h2c"}, 400},
		"post":              {"POST", "/bus", up, 405},
		"other path":        {"GET", "/bus/other", up, 404},
		"the server root":   {"GET", "/", up, 404},
		"a monitoring path": {"GET", "/bus/varz", up, 404},
	} {
		if got := do(c.method, c.path, c.hdr); got != c.want {
			t.Errorf("%s: answered %d, want %d", name, got, c.want)
		}
	}
	select {
	case <-seen:
		t.Fatal("a refused request reached the server")
	default:
	}
	hdr := map[string]string{"Cookie": "a=b", "Authorization": "Bearer x", "X-Forwarded-For": "6.6.6.6", "Forwarded": "for=6.6.6.6", "Origin": "https://evil.example"}
	maps.Copy(hdr, up)
	do("GET", wsproxy.LeafPath, hdr) // the backend answers 418, not an upgrade
	r := <-seen
	if r.URL.Path != "/leafnode" {
		t.Errorf("the leaf path went to %q", r.URL.Path)
	}
	for _, k := range []string{"Cookie", "Authorization", "Forwarded", "Origin"} {
		if v := r.Header.Get(k); v != "" {
			t.Errorf("header %s reached the server: %q", k, v)
		}
	}
	if got := r.Header.Get("X-Forwarded-For"); strings.Contains(got, "6.6.6.6") {
		t.Errorf("a client-set address reached the server: %q", got)
	}
}

func TestOnlyLoopbackTargetsAreAccepted(t *testing.T) {
	for _, bad := range []string{"", "example.com:80", "10.0.0.5:4222", "8.8.8.8:80", "127.0.0.1", "[::]:80"} {
		if _, err := wsproxy.New(bad, wsproxy.DefaultConfig(), slog.New(slog.DiscardHandler)); !errors.Is(err, wsproxy.ErrNotLoopback) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, ok := range []string{"127.0.0.1:1", "[::1]:1", "localhost:1"} {
		if _, err := wsproxy.New(ok, wsproxy.DefaultConfig(), slog.New(slog.DiscardHandler)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestDeadServerIsBadGateway(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	h, _ := wsproxy.New(addr, wsproxy.DefaultConfig(), slog.New(slog.DiscardHandler))
	req := httptest.NewRequest("GET", "/bus", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("answered %d", w.Code)
	}
}
