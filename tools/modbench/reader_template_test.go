//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sandbox"
)

// TestMain lets the test binary be the sandbox's helper, as the ganglion
// binary is: the template is built in the command sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	os.Exit(m.Run())
}

const templateDir = "../../examples/modules/reader-template"

// The reader template, built once by the node's own builder, run on recorded
// answers of its example service. Its build fetches crates from crates.io, so
// it runs only with GANGLION_NET_TESTS=1 (make test-net, docs/conventions.md
// section 9).
func TestTheReaderTemplate(t *testing.T) {
	if os.Getenv("GANGLION_NET_TESTS") == "" {
		t.Skip("it fetches crates from crates.io: set GANGLION_NET_TESTS=1 (make test-net)")
	}
	art := buildTemplate(t)
	t.Run("reads the recorded answers", func(t *testing.T) { readsTheRecordedAnswers(t, art) })
	t.Run("is measured end to end", func(t *testing.T) { isMeasuredEndToEnd(t, art) })
	t.Run("waits out Retry-After", func(t *testing.T) { waitsOutRetryAfter(t, art) })
}

// R-RDR-1 and R-RDR-3 through the runner: the cells hold the recorded
// reading, the status tool answers within its contract, and init and health
// make no request.
func readsTheRecordedAnswers(t *testing.T, art builder.Artifact) {
	now := time.Now().Truncate(time.Second)

	srv, hits := replayServer(t, "testdata/reader-replay.json", now)
	cells := runner.NewMemCells(func() time.Time { return now })
	mod := load(t, art.Wasm, art.Manifest, srv.URL, cells)
	if err := mod.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := mod.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("init and health made %d requests", n)
	}
	out := handle(t, mod, "cron")
	got := cells.Values()
	t.Logf("the template's cells: %s", got)

	want := map[string]any{"ts": float64(now.Unix()), "value": 21.5, "online": true}
	if sample := decode(t, got["reader-template.sample"]); !reflect.DeepEqual(sample, want) {
		t.Fatalf("the sample cell\n%v\ndiffers from the recorded reading\n%v", sample, want)
	}
	if !reflect.DeepEqual(decode(t, out.Result), want) {
		t.Errorf("the cron answer %s is not the reading", out.Result)
	}
	if v := value(t, got["reader-template.value"]); v != 21.5 {
		t.Errorf("value cell %v, want 21.5", v)
	}
	if v := value(t, got["reader-template.online"]); v != true {
		t.Errorf("online cell %v, want true", v)
	}

	status := decode(t, handle(t, mod, "tool:status").Result)
	if status["fresh"] != true || !reflect.DeepEqual(status["sample"], want) {
		t.Errorf("status = %v, want a fresh answer with the same reading", status)
	}
}

// modbench itself, on the template: five calls against the replay, a report
// with every measure filled in.
func isMeasuredEndToEnd(t *testing.T, art builder.Artifact) {
	wasm := filepath.Join(t.TempDir(), "reader.wasm")
	if err := os.WriteFile(wasm, art.Wasm, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-wasm", wasm, "-manifest", templateManifest,
		"-replay", "testdata/reader-replay.json", "-config", "station=roof-1", "-n", "5",
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
	if !strings.Contains(r.LastResult, `"value"`) {
		t.Fatalf("last result %q lacks a reading", r.LastResult)
	}
}

// R-RDR-2 through the runner: a 429 with Retry-After 120 is answered OK,
// and no request follows within the wait, from cron or from the tool.
func waitsOutRetryAfter(t *testing.T, art builder.Artifact) {
	now := time.Now().Truncate(time.Second)
	srv, hits := replayServer(t, "testdata/reader-replay-429.json", now)
	state := runner.NewMemState()
	rust := loadWith(t, art.Wasm, art.Manifest, srv.URL, runner.NewMemCells(time.Now), state)

	handle(t, rust, "cron")
	if n := hits.Load(); n != 1 {
		t.Fatalf("the first run made %d requests, want one read", n)
	}
	raw, _ := state.Get("reader-template", "not_before")
	until, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || until < now.Unix()+119 || until > time.Now().Unix()+121 {
		t.Fatalf("not_before = %q, want about now + 120 s", raw)
	}
	handle(t, rust, "cron")
	out, err := rust.Handle(t.Context(), pdk.Input{Trigger: "tool:status"})
	if err != nil || out.OK {
		t.Fatalf("status with no reading = %+v, %v; want a failure", out, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d requests within Retry-After", n-1)
	}
}

// buildExample builds the Rust example with the node's local builder in
// this machine's command sandbox, or skips naming what is missing.
func buildTemplate(t *testing.T) builder.Artifact {
	t.Helper()
	b := builder.NewLocal(realSandbox(t), rustConfig(t))
	can, err := b.Can(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if can[builder.Rust] == "" {
		t.Skip("the wasm32-wasip1 target is not installed (`rustup target add wasm32-wasip1`)")
	}
	art, log, err := b.Build(t.Context(), builder.Source{Name: "reader-template", Dir: templateDir})
	if err != nil && offline(string(log)) {
		t.Skipf("crates.io is out of reach:\n%s", log)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	t.Logf("built the template: %d bytes, sha %s, %s", len(art.Wasm), art.SHA256, can[builder.Rust])
	return art
}

func offline(log string) bool {
	for _, s := range []string{"failed to download", "Couldn't resolve host", "failed to query replaced source"} {
		if strings.Contains(log, s) {
			return true
		}
	}
	return false
}

// realSandbox is the command sandbox of this machine, or the test is
// skipped (as in the builder's tests).
func realSandbox(t *testing.T) *sandbox.Sandbox {
	t.Helper()
	was, err := sandbox.HostKernel().Dumpable()
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Harden(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.SetDumpable(was) })
	home, _ := os.UserHomeDir()
	node := sandbox.NodeFiles{DB: filepath.Join(t.TempDir(), "node.db")}
	s := sandbox.New("/proc/self/exe", sandbox.Policy{
		Deny: func() []string { return sandbox.DenyList(home, "/", node, nil, nil) },
		Env:  []string{"LANG=C.UTF-8", "HOME=" + home},
	}, sandbox.HostKernel(), time.Now())
	if ok, why := s.Ready(); !ok {
		t.Skipf("the command sandbox is not ready here: %s", why)
	}
	return s
}

// rustConfig finds cargo the way the node's command line does, pins the
// toolchain rustc names from "/", and builds in a root of the test's own
// under the user's cache folder; or skips.
func rustConfig(t *testing.T) builder.LocalConfig {
	t.Helper()
	home, _ := os.UserHomeDir()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		cargo = filepath.Join(home, ".cargo", "bin", "cargo")
		if _, err := os.Stat(cargo); err != nil {
			t.Skip("cargo is not installed (rustup, then `rustup target add wasm32-wasip1`)")
		}
	}
	cfg := builder.DefaultLocalConfig()
	cfg.Cargo, cfg.Rustup = cargo, os.Getenv("RUSTUP_HOME")
	if cfg.Rustup == "" {
		cfg.Rustup = filepath.Join(home, ".rustup")
	}
	cmd := exec.CommandContext(t.Context(), filepath.Join(filepath.Dir(cargo), "rustc"), "--print", "sysroot")
	cmd.Dir = "/"
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("rustc does not name its toolchain: %v", err)
	}
	cfg.Toolchain = filepath.Base(strings.TrimSpace(string(out)))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no cache folder: %v", err)
	}
	base := filepath.Join(cache, "ganglion", "builds-test")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if cfg.TempRoot, err = os.MkdirTemp(base, "t-"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfg.TempRoot) })
	return cfg
}

// replayServer serves a replay file with __NOW__ fixed at now, counting the
// requests.
func replayServer(t *testing.T, path string, now time.Time) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newReplay(src)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }
	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		r.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func load(t *testing.T, wasm, manifest []byte, target string, cells runner.CellPort) *runner.Module {
	t.Helper()
	return loadWith(t, wasm, manifest, target, cells, runner.NewMemState())
}

// loadWith loads a module under manifest with the serial number of the
// replay, placeholder secrets, and every host answered by target.
func loadWith(t *testing.T, wasm, manifest []byte, target string, cells runner.CellPort, state runner.State) *runner.Module {
	t.Helper()
	m, err := runner.ParseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	m.ConfigValues = map[string]string{"station": "roof-1"}
	mod, err := runner.Load(t.Context(), wasm, m, runner.Deps{
		Secrets: func(name string) (string, bool) { return "test-" + strings.ToLower(name), true },
		State:   state,
		Cells:   cells,
		HTTP:    &http.Client{Transport: redirect(target), Timeout: m.Limits.Timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mod.Close(context.Background()) })
	return mod
}

// handle runs one call that must succeed.
func handle(t *testing.T, mod *runner.Module, trigger string) pdk.Output {
	t.Helper()
	out, err := mod.Handle(t.Context(), pdk.Input{Trigger: trigger})
	if err != nil || !out.OK {
		t.Fatalf("%s: %+v, %v", trigger, out, err)
	}
	return out
}

// value decodes a JSON value.
func value(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

// decode decodes a JSON object.
func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	m, ok := value(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", raw)
	}
	return m
}
