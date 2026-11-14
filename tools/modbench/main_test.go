package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// templateManifest is the manifest of the reader template.
const templateManifest = "../../examples/modules/reader-template/manifest.yaml"

func TestReplayServesRecordedBodiesWithCurrentTime(t *testing.T) {
	h, err := newReplay([]byte(`[{"path": "/v1/a", "body": {"ts": __NOW__}}, {"path": "/v1/b", "body": "plain"}]`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := get(t, srv.URL+"/v1/a")
	var got struct{ TS int64 }
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if time.Since(time.Unix(got.TS, 0)).Abs() > time.Minute {
		t.Errorf("ts = %d, want about now", got.TS)
	}
	if body := get(t, srv.URL+"/v1/b"); body != `"plain"` {
		t.Errorf("/v1/b = %q", body)
	}
	resp, err := http.Get(srv.URL + "/v1/missing")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", resp.StatusCode)
	}
}

func TestReplayEntriesSetTheirStatusAndHeaders(t *testing.T) {
	h, err := newReplay([]byte(`[{"path": "/v1/latest", "status": 429, "headers": {"Retry-After": "120"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/latest")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "120" {
		t.Fatalf("answer = %d, Retry-After %q; want 429 and 120", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestAReplayWithARepeatedPathIsRefused(t *testing.T) {
	for _, src := range []string{`[{"path": "/a"}, {"path": "/a"}]`, `[{"body": 1}]`, `{"/a": 1}`} {
		if _, err := newReplay([]byte(src)); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
}

func TestRedirectSendsEveryHostToTheReplayServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path+"?"+r.URL.RawQuery)
	}))
	defer srv.Close()
	client := &http.Client{Transport: redirect(srv.URL)}
	resp, err := client.Get("https://api.example.com/v1/stations/roof-1/latest?unit=c")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "/v1/stations/roof-1/latest?unit=c" {
		t.Fatalf("server saw %q", b)
	}
}

func TestBenchToolModuleWithASavedPage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-wasm", "../../modules/html2md/html2md.wasm", "-manifest", "../../modules/html2md/html2md.yaml",
		"-html", "../../modules/html2md/testdata/table.html", "-n", "3",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	var r report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("report %s: %v", stdout.String(), err)
	}
	if r.Calls != 3 || r.Failures != 0 || !strings.Contains(r.LastResult, "US Dollar") {
		t.Fatalf("report = %+v", r)
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}
