package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/testinputs"
)

// exampleManifest is the manifest of the Rust example, which the Go
// reference reader runs under too.
const exampleManifest = "../../examples/modules/deye-reader/manifest.yaml"

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
	resp, err := client.Get("https://eu1-developer.deyecloud.com/v1.0/account/token?appId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "/v1.0/account/token?appId=1" {
		t.Fatalf("server saw %q", b)
	}
}

func TestBenchDeyeModuleEndToEnd(t *testing.T) {
	dir := t.TempDir()
	wasm := filepath.Join(dir, "deye.wasm")
	if err := os.WriteFile(wasm, buildReference(t), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-wasm", wasm, "-manifest", exampleManifest,
		"-replay", "testdata/deye-replay.json", "-config", "inverter_sn=2400000001", "-n", "5",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	var r report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("report %s: %v", stdout.String(), err)
	}
	if r.Calls != 5 || r.Failures != 0 || r.WasmBytes == 0 || r.CallP50Micros <= 0 || r.RSSAfterCallsMiB <= 0 {
		t.Fatalf("report = %+v", r)
	}
	if r.RSSPeakMiB < r.RSSAfterCallsMiB {
		t.Fatalf("peak RSS %.1f MiB is below the final RSS %.1f MiB", r.RSSPeakMiB, r.RSSAfterCallsMiB)
	}
	if !strings.Contains(r.LastResult, `"soc"`) {
		t.Fatalf("last result %q lacks a sample", r.LastResult)
	}
}

// buildReference builds the Go reference reader, internal/deyeref, as a
// module.
func buildReference(t *testing.T) []byte {
	t.Helper()
	testinputs.Build(t, "../../internal/deyeref/wasm", "GOOS=wasip1", "GOARCH=wasm")
	wasm := filepath.Join(t.TempDir(), "deyeref.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasm, "../../internal/deyeref/wasm")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	b, err := os.ReadFile(wasm)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
