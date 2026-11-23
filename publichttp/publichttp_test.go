package publichttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// now is an AfterFunc that does not wait.
func now(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	c <- time.Time{}
	return c
}

// started is a running listener on a free port and its base URL.
func started(t *testing.T, cfg Config) (*Listener, string) {
	t.Helper()
	l, err := New(cfg, slog.New(slog.DiscardHandler), now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return l, "http://" + l.Addr(t.Context())
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // a test against its own listener
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Abuse: the listener has no page but the registered ones; a probe for
// anything else learns nothing (ADR-0078).
func TestOnlyRegisteredPathsAnswer(t *testing.T) {
	rec := telemetrytest.Install(t)
	l, err := New(DefaultConfig("127.0.0.1:0"), slog.New(slog.DiscardHandler), now)
	if err != nil {
		t.Fatal(err)
	}
	l.Handle("/hook", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	ts := httptest.NewServer(l)
	defer ts.Close()
	for path, want := range map[string]int{
		"/hook": http.StatusAccepted, HealthPath: http.StatusOK,
		"/": http.StatusNotFound, "/metrics": http.StatusNotFound, "/hook/": http.StatusNotFound,
		"/hook/../metrics": http.StatusNotFound, "/webmetrics/": http.StatusNotFound, "/.env": http.StatusNotFound,
	} {
		if code, _ := get(t, ts.URL+path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if n := rec.Value(t, "ganglion.http.requests", map[string]string{"route": "other", "code": "404"}); n != 6 {
		t.Errorf("unregistered paths counted %v times under route other, want 6", n)
	}
	if n := rec.Value(t, "ganglion.http.requests", map[string]string{"route": "/hook", "code": "202"}); n != 1 {
		t.Errorf("/hook counted %v times, want 1", n)
	}
}

// Abuse: a body past the cap fails the handler's read, and a request past
// the cap of requests in flight is turned away.
func TestLimits(t *testing.T) {
	cfg := DefaultConfig("127.0.0.1:0")
	cfg.MaxBody, cfg.MaxInFlight = 8, 1
	l, err := New(cfg, slog.New(slog.DiscardHandler), now)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	l.Handle("/read", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	}))
	l.Handle("/slow", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release }))
	ts := httptest.NewServer(l)
	defer ts.Close()

	post := func(body string) int {
		resp, err := http.Post(ts.URL+"/read", "text/plain", strings.NewReader(body)) //nolint:noctx // a test
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("12345678"); code != http.StatusOK {
		t.Errorf("a body at the cap: %d", code)
	}
	if code := post("123456789"); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body past the cap: %d", code)
	}

	slow := make(chan struct{})
	go func() { defer close(slow); get(t, ts.URL+"/slow") }()
	<-entered
	if code, _ := get(t, ts.URL+"/read"); code != http.StatusServiceUnavailable {
		t.Errorf("a request past the cap in flight: %d, want 503", code)
	}
	close(release)
	<-slow
}

func TestReachesItsOwnAddress(t *testing.T) {
	l, base := started(t, DefaultConfig("127.0.0.1:0"))
	if err := l.Reaches(t.Context(), base+"/"); err != nil {
		t.Fatalf("its own address: %v", err)
	}
}

// The spike's finding: during a deploy the address answered 200 from the
// process this one replaces. Only this start's value counts.
func TestAnotherProcessAnsweringIsUnreachable(t *testing.T) {
	l, _ := started(t, DefaultConfig("127.0.0.1:0"))
	asked := 0
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked++
		_, _ = io.WriteString(w, "the older process's value")
	}))
	defer old.Close()
	err := l.Reaches(t.Context(), old.URL)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if asked != l.cfg.CheckTries {
		t.Errorf("asked %d times, want %d", asked, l.cfg.CheckTries)
	}
	for name, h := range map[string]http.HandlerFunc{
		"not found": http.NotFound,
		"a redirect to itself": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://"+l.Addr(t.Context())+HealthPath, http.StatusFound)
		},
	} {
		other := httptest.NewServer(h)
		if err := l.Reaches(t.Context(), other.URL); !errors.Is(err, ErrUnreachable) {
			t.Errorf("%s: err = %v, want ErrUnreachable", name, err)
		}
		other.Close()
	}
}

// The address answers as this process a moment after the start.
func TestReachesAfterTheAddressMoves(t *testing.T) {
	l, base := started(t, DefaultConfig("127.0.0.1:0"))
	tries := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tries++; tries < 3 {
			_, _ = io.WriteString(w, "old")
			return
		}
		_, body := get(t, base+r.URL.Path)
		_, _ = io.WriteString(w, body)
	}))
	defer proxy.Close()
	if err := l.Reaches(t.Context(), proxy.URL); err != nil {
		t.Fatalf("err = %v after %d tries", err, tries)
	}
}

func TestBusyAddressFailsRun(t *testing.T) {
	_, base := started(t, DefaultConfig("127.0.0.1:0"))
	l, err := New(DefaultConfig(strings.TrimPrefix(base, "http://")), slog.New(slog.DiscardHandler), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Run(t.Context()); err == nil {
		t.Fatal("Run on a busy address returned nil")
	}
}

// A stream route is not counted against MaxInFlight, so long tunnels cannot
// starve the short routes; the short routes still hit their limit, and the
// stream's request ends when the listener stops (ADR-0114).
func TestStreamRoutesAreNotCountedAndEndWithTheListener(t *testing.T) {
	cfg := DefaultConfig("127.0.0.1:0")
	cfg.MaxInFlight = 1
	l, err := New(cfg, slog.New(slog.DiscardHandler), now)
	if err != nil {
		t.Fatal(err)
	}
	open, ended := make(chan struct{}, 8), make(chan struct{}, 8)
	l.HandleStream("/s", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		open <- struct{}{}
		<-r.Context().Done()
		ended <- struct{}{}
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	base := "http://" + l.Addr(t.Context())
	for range 3 { // more streams than MaxInFlight
		resp, err := http.Get(base + "/s") //nolint:noctx // a test against its own listener
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		<-open
	}
	if code, _ := get(t, base+HealthPath); code != http.StatusOK {
		t.Fatalf("open streams starved a short route: %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("a stream's request did not end with the listener")
		}
	}
}
