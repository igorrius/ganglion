package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
)

var probeWASM []byte

// TestMain compiles the probe module once with the standard Go toolchain.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ganglion-runner")
	if err != nil {
		panic(err)
	}
	out := filepath.Join(dir, "probe.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./testdata/probe")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build probe module: %v\n%s", err, b))
	}
	if probeWASM, err = os.ReadFile(out); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	manifest Manifest
	secrets  map[string]string
	state    *MemState
	logs     strings.Builder
}

func newEnv() *env {
	return &env{
		manifest: Manifest{
			Name:    "probe",
			Version: 1,
			Limits:  Limits{Memory: 64 << 20, Timeout: 5 * time.Second},
		},
		secrets: map[string]string{},
		state:   NewMemState(),
	}
}

func (e *env) load(t *testing.T) *Module {
	t.Helper()
	ctx := context.Background()
	mod, err := Load(ctx, probeWASM, e.manifest, Deps{
		Secrets: func(n string) (string, bool) { v, ok := e.secrets[n]; return v, ok },
		State:   e.state,
		HTTP:    http.DefaultClient,
		Logger:  slog.New(slog.NewTextHandler(&e.logs, nil)),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = mod.Close(ctx) })
	return mod
}

func call(t *testing.T, mod *Module, payload string) pdk.Output {
	t.Helper()
	out, err := mod.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(payload)})
	if err != nil {
		t.Fatalf("Handle(%s): %v", payload, err)
	}
	return out
}

func result(t *testing.T, out pdk.Output) map[string]any {
	t.Helper()
	if !out.OK {
		t.Fatalf("module failed: %s %s", out.Kind, out.Error)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Result, &m); err != nil {
		t.Fatalf("result %s: %v", out.Result, err)
	}
	return m
}

func TestHandlePassesTheTrigger(t *testing.T) {
	mod := newEnv().load(t)
	if got := result(t, call(t, mod, `{"op":"echo"}`))["trigger"]; got != "cron" {
		t.Fatalf("trigger = %v, want cron", got)
	}
}

func TestFetchToAllowedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "hello")
	}))
	defer srv.Close()
	e := newEnv()
	e.manifest.Capabilities.HTTPFetch = []string{"127.0.0.1"}

	r := result(t, call(t, e.load(t), fmt.Sprintf(`{"op":"fetch","url":%q}`, srv.URL)))
	if r["status"] != float64(200) || r["body"] != "hello" {
		t.Fatalf("fetch result = %v, want 200 hello", r)
	}
}

func TestFetchToUnlistedHostIsDenied(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	e := newEnv()
	e.manifest.Capabilities.HTTPFetch = []string{"example.com"}

	out := call(t, e.load(t), fmt.Sprintf(`{"op":"fetch","url":%q}`, srv.URL))
	if out.OK || !strings.Contains(out.Error, "not allowed") {
		t.Fatalf("output = %+v, want a 'not allowed' failure", out)
	}
	if hit {
		t.Fatal("the request reached the server")
	}
}

func TestSecretOnlyWhenGranted(t *testing.T) {
	e := newEnv()
	e.secrets = map[string]string{"GRANTED": "yes", "OTHER": "no"}
	e.manifest.Capabilities.SecretGet = []string{"GRANTED"}
	mod := e.load(t)

	if r := result(t, call(t, mod, `{"op":"secret","name":"GRANTED"}`)); r["ok"] != true || r["value"] != "yes" {
		t.Errorf("granted secret = %v, want yes", r)
	}
	if r := result(t, call(t, mod, `{"op":"secret","name":"OTHER"}`)); r["ok"] != false || r["value"] != "" {
		t.Errorf("ungranted secret = %v, want nothing", r)
	}
}

func TestConfigFromManifest(t *testing.T) {
	e := newEnv()
	e.manifest.Config = map[string]string{"inverter_sn": "123"}
	r := result(t, call(t, e.load(t), `{"op":"config","name":"inverter_sn"}`))
	if r["ok"] != true || r["value"] != "123" {
		t.Fatalf("config = %v, want 123", r)
	}
}

func TestStatePersistsAcrossCalls(t *testing.T) {
	e := newEnv()
	e.manifest.Capabilities.State = true
	mod := e.load(t)

	call(t, mod, `{"op":"state_put","name":"token","value":"abc"}`)
	if r := result(t, call(t, mod, `{"op":"state_get","name":"token"}`)); r["value"] != "abc" {
		t.Fatalf("state_get = %v, want abc", r)
	}
	if v, _ := e.state.Get("probe", "token"); v != "abc" {
		t.Fatalf("store holds %q for probe/token, want abc", v)
	}
}

func TestStateDeniedWithoutCapability(t *testing.T) {
	e := newEnv()
	mod := e.load(t)
	call(t, mod, `{"op":"state_put","name":"token","value":"abc"}`)
	if r := result(t, call(t, mod, `{"op":"state_get","name":"token"}`)); r["ok"] != false {
		t.Fatalf("state_get without capability = %v, want nothing", r)
	}
}

func TestNowAndLog(t *testing.T) {
	e := newEnv()
	mod := e.load(t)
	r := result(t, call(t, mod, `{"op":"now"}`))
	if got := int64(r["unix"].(float64)); time.Since(time.Unix(got, 0)).Abs() > time.Minute {
		t.Errorf("now = %v, want about the current time", got)
	}
	call(t, mod, `{"op":"log","value":"hello from wasm"}`)
	if !strings.Contains(e.logs.String(), "hello from wasm") {
		t.Errorf("log output lacks the module's line:\n%s", e.logs.String())
	}
}

func TestModuleErrorKeepsItsKind(t *testing.T) {
	out := call(t, newEnv().load(t), `{"op":"fail","value":"bad password"}`)
	if out.OK || out.Kind != pdk.KindAuthFailed || !strings.Contains(out.Error, "bad password") {
		t.Fatalf("output = %+v, want auth_failed with the message", out)
	}
}

func TestTimeoutStopsASpinningModule(t *testing.T) {
	e := newEnv()
	e.manifest.Limits.Timeout = 200 * time.Millisecond
	mod := e.load(t)

	start := time.Now()
	_, err := mod.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"spin"}`)})
	if err == nil {
		t.Fatal("spinning module returned no error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Handle took %v, want it stopped near the 200 ms timeout", d)
	}
	// The next call gets a fresh instance and works.
	call(t, mod, `{"op":"echo"}`)
}

func TestMemoryLimitStopsAGreedyModule(t *testing.T) {
	e := newEnv()
	e.manifest.Limits.Memory = 32 << 20
	mod := e.load(t)

	_, err := mod.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"alloc","mib":64}`)})
	if err == nil {
		t.Fatal("allocating 64 MiB under a 32 MiB limit succeeded")
	}
	call(t, mod, `{"op":"echo"}`)
}
