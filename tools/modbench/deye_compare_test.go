//go:build linux

package main

import (
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
// binary is: the Rust example is built in the command sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	os.Exit(m.Run())
}

const exampleDir = "../../examples/modules/deye-reader"

// The Rust example, built once by the node's own builder, run on the
// recorded DeyeCloud answers. Its build fetches crates from crates.io, so it
// runs only with GANGLION_NET_TESTS=1 (make test-net, docs/conventions.md
// section 9).
func TestTheRustExample(t *testing.T) {
	if os.Getenv("GANGLION_NET_TESTS") == "" {
		t.Skip("it fetches crates from crates.io: set GANGLION_NET_TESTS=1 (make test-net)")
	}
	art := buildExample(t)
	t.Run("agrees with the Go reference", func(t *testing.T) { agreesWithTheReference(t, art) })
	t.Run("waits out Retry-After", func(t *testing.T) { waitsOutRetryAfter(t, art) })
}

// R-LS-4's spirit: the example and the Go reference reader read the same
// answers and agree. The example's sample cell must equal the reference's
// sample, its status tool must answer within its contract, and init and
// health must make no request.
func agreesWithTheReference(t *testing.T, art builder.Artifact) {
	now := time.Now().Truncate(time.Second)

	srv, hits := replayServer(t, "testdata/deye-replay.json", now)
	cells := runner.NewMemCells(func() time.Time { return now })
	rust := load(t, art.Wasm, art.Manifest, srv.URL, cells)
	if err := rust.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rust.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("init and health made %d requests", n)
	}
	out := handle(t, rust, "cron")
	got := cells.Values()
	t.Logf("the example's cells: %s", got)

	manifest, err := os.ReadFile(exampleManifest)
	if err != nil {
		t.Fatal(err)
	}
	ref := handle(t, load(t, buildReference(t), manifest, srv.URL, runner.NewMemCells(time.Now)), "cron")
	t.Logf("the reference's sample: %s", ref.Result)

	want := decode(t, ref.Result)
	if sample := decode(t, got["deye-reader.sample"]); !reflect.DeepEqual(sample, want) {
		t.Fatalf("the example's sample cell\n%v\ndiffers from the reference's\n%v", sample, want)
	}
	if !reflect.DeepEqual(decode(t, out.Result), want) {
		t.Errorf("the example's cron answer %s is not its sample", out.Result)
	}
	if soc := value(t, got["deye-reader.soc"]); soc != want["soc"] {
		t.Errorf("soc cell %v, sample soc %v", soc, want["soc"])
	}
	if grid := value(t, got["deye-reader.grid"]); grid != (want["grid_on"] == 1.0) {
		t.Errorf("grid cell %v, sample grid_on %v", grid, want["grid_on"])
	}

	status := decode(t, handle(t, rust, "tool:status").Result)
	if status["fresh"] != true || !reflect.DeepEqual(status["sample"], want) {
		t.Errorf("status = %v, want a fresh answer with the same sample", status)
	}
}

// R-DEYE-2 through the runner: a 429 with Retry-After 120 is answered OK,
// and no request follows within the wait, from cron or from the tool.
func waitsOutRetryAfter(t *testing.T, art builder.Artifact) {
	now := time.Now().Truncate(time.Second)
	srv, hits := replayServer(t, "testdata/deye-replay-429.json", now)
	state := runner.NewMemState()
	rust := loadWith(t, art.Wasm, art.Manifest, srv.URL, runner.NewMemCells(time.Now), state)

	handle(t, rust, "cron")
	if n := hits.Load(); n != 2 {
		t.Fatalf("the first run made %d requests, want a login and a read", n)
	}
	raw, _ := state.Get("deye-reader", "not_before")
	until, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || until < now.Unix()+119 || until > time.Now().Unix()+121 {
		t.Fatalf("not_before = %q, want about now + 120 s", raw)
	}
	handle(t, rust, "cron")
	out, err := rust.Handle(t.Context(), pdk.Input{Trigger: "tool:status"})
	if err != nil || out.OK {
		t.Fatalf("status with no reading = %+v, %v; want a failure", out, err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d requests within Retry-After", n-2)
	}
}

// buildExample builds the Rust example with the node's local builder in
// this machine's command sandbox, or skips naming what is missing.
func buildExample(t *testing.T) builder.Artifact {
	t.Helper()
	b := builder.NewLocal(realSandbox(t), rustConfig(t))
	can, err := b.Can(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if can[builder.Rust] == "" {
		t.Skip("the wasm32-wasip1 target is not installed (`rustup target add wasm32-wasip1`)")
	}
	art, log, err := b.Build(t.Context(), builder.Source{Name: "deye-reader", Dir: exampleDir})
	if err != nil && offline(string(log)) {
		t.Skipf("crates.io is out of reach:\n%s", log)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	t.Logf("built the example: %d bytes, sha %s, %s", len(art.Wasm), art.SHA256, can[builder.Rust])
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
	m.ConfigValues = map[string]string{"inverter_sn": "2400000001"}
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
