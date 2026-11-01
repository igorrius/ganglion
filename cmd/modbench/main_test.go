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
)

func TestReplayServesRecordedBodiesWithCurrentTime(t *testing.T) {
	h, err := newReplay([]byte(`{"/v1/a": {"ts": __NOW__}, "/v1/b": "plain"}`))
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
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasm, "../../modules/deye/own")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-wasm", wasm, "-abi", "own", "-manifest", "../../modules/deye/deye-reader.yaml",
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
	if !strings.Contains(r.LastResult, `"soc"`) {
		t.Fatalf("last result %q lacks a sample", r.LastResult)
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
