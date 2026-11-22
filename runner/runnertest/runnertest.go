// Package runnertest is the conformance suite every module host must pass:
// each host function, each manifest restriction and each limit, exercised
// through the probe module.
package runnertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// Instance is a loaded module.
type Instance interface {
	Handle(ctx context.Context, in pdk.Input) (pdk.Output, error)
	Init(ctx context.Context) error
	Health(ctx context.Context) error
	Close(ctx context.Context) error
}

// Loader loads a module the way a host under test does.
type Loader func(ctx context.Context, wasm []byte, m runner.Manifest, deps runner.Deps) (Instance, error)

// BuildProbe compiles the probe module with the standard Go toolchain and
// returns the .wasm bytes.
func BuildProbe() ([]byte, error) {
	dir, err := os.MkdirTemp("", "ganglion-probe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "probe.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out,
		"github.com/igorrius/ganglion/runner/runnertest/probe/wasm")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build probe: %v\n%s", err, b)
	}
	return os.ReadFile(out)
}

type env struct {
	load     Loader
	wasm     []byte
	manifest runner.Manifest
	secrets  map[string]string
	state    runner.State
	cells    *fakeCells
	notify   *fakeNotify
	emit     *fakeEmit
	call     *fakeCall
	logs     syncBuffer
}

func (e *env) instance(t *testing.T) Instance {
	t.Helper()
	ctx := context.Background()
	inst, err := e.load(ctx, e.wasm, e.manifest, runner.Deps{
		Secrets: func(n string) (string, bool) { v, ok := e.secrets[n]; return v, ok },
		State:   e.state,
		Cells:   e.cells,
		Notify:  e.notify,
		Emit:    e.emit,
		Call:    e.call,
		Prefix:  "probe-mod",
		HTTP:    http.DefaultClient,
		Logger:  slog.New(slog.NewTextHandler(&e.logs, nil)),
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close(ctx) })
	return inst
}

func call(t *testing.T, inst Instance, payload string) pdk.Output {
	t.Helper()
	out, err := inst.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(payload)})
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

// Run runs the suite against a host. probe is the module from BuildProbe.
func Run(t *testing.T, load Loader, probe []byte) {
	fresh := func() *env {
		return &env{
			load: load,
			wasm: probe,
			manifest: runner.Manifest{
				Name:    "probe",
				Version: 1,
				Limits:  runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second},
			},
			secrets: map[string]string{},
			state:   runner.NewMemState(),
			cells:   &fakeCells{values: map[string]cellValue{}},
			notify:  &fakeNotify{},
			emit:    &fakeEmit{},
			call:    &fakeCall{},
		}
	}

	t.Run("passes the trigger", func(t *testing.T) {
		if got := result(t, call(t, fresh().instance(t), `{"op":"echo"}`))["trigger"]; got != "cron" {
			t.Fatalf("trigger = %v, want cron", got)
		}
	})

	t.Run("fetch to an allowed host", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, "hello")
		}))
		defer srv.Close()
		e := fresh()
		e.manifest.Capabilities.HTTPFetch = []string{"127.0.0.1"}
		r := result(t, call(t, e.instance(t), fmt.Sprintf(`{"op":"fetch","url":%q}`, srv.URL)))
		if r["status"] != float64(200) || r["body"] != "hello" {
			t.Fatalf("fetch result = %v, want 200 hello", r)
		}
	})

	t.Run("fetch to an unlisted host is denied", func(t *testing.T) {
		var hit bool
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
		defer srv.Close()
		e := fresh()
		e.manifest.Capabilities.HTTPFetch = []string{"example.com"}
		out, err := e.instance(t).Handle(context.Background(),
			pdk.Input{Trigger: "cron", Payload: json.RawMessage(fmt.Sprintf(`{"op":"fetch","url":%q}`, srv.URL))})
		if err == nil && out.OK {
			t.Fatalf("output = %+v, want a failure", out)
		}
		if hit {
			t.Fatal("the request reached the server")
		}
		t.Logf("denial surfaces as: err=%v, output=%+v", err, out)
	})

	t.Run("secret only when granted", func(t *testing.T) {
		e := fresh()
		e.secrets = map[string]string{"GRANTED": "yes", "OTHER": "no"}
		e.manifest.Capabilities.SecretGet = []string{"GRANTED"}
		inst := e.instance(t)
		if r := result(t, call(t, inst, `{"op":"secret","name":"GRANTED"}`)); r["ok"] != true || r["value"] != "yes" {
			t.Errorf("granted secret = %v, want yes", r)
		}
		if r := result(t, call(t, inst, `{"op":"secret","name":"OTHER"}`)); r["ok"] != false || r["value"] != "" {
			t.Errorf("ungranted secret = %v, want nothing", r)
		}
	})

	t.Run("config from the manifest", func(t *testing.T) {
		e := fresh()
		e.manifest.ConfigValues = map[string]string{"inverter_sn": "123"}
		r := result(t, call(t, e.instance(t), `{"op":"config","name":"inverter_sn"}`))
		if r["ok"] != true || r["value"] != "123" {
			t.Fatalf("config = %v, want 123", r)
		}
	})

	t.Run("state persists across calls", func(t *testing.T) {
		e := fresh()
		e.manifest.Capabilities.State = true
		inst := e.instance(t)
		call(t, inst, `{"op":"state_put","name":"token","value":"abc"}`)
		if r := result(t, call(t, inst, `{"op":"state_get","name":"token"}`)); r["value"] != "abc" {
			t.Fatalf("state_get = %v, want abc", r)
		}
		if v, _ := e.state.Get("probe", "token"); v != "abc" {
			t.Fatalf("store holds %q for probe/token, want abc", v)
		}
	})

	t.Run("state denied without the capability", func(t *testing.T) {
		inst := fresh().instance(t)
		call(t, inst, `{"op":"state_put","name":"token","value":"abc"}`)
		if r := result(t, call(t, inst, `{"op":"state_get","name":"token"}`)); r["ok"] != false {
			t.Fatalf("state_get without capability = %v, want nothing", r)
		}
	})

	t.Run("now and log", func(t *testing.T) {
		e := fresh()
		inst := e.instance(t)
		r := result(t, call(t, inst, `{"op":"now"}`))
		if got := int64(r["unix"].(float64)); time.Since(time.Unix(got, 0)).Abs() > time.Minute {
			t.Errorf("now = %v, want about the current time", got)
		}
		call(t, inst, `{"op":"log","value":"hello from wasm"}`)
		if !strings.Contains(e.logs.String(), "hello from wasm") {
			t.Errorf("log output lacks the module's line:\n%s", e.logs.String())
		}
	})

	t.Run("module error keeps its kind", func(t *testing.T) {
		out := call(t, fresh().instance(t), `{"op":"fail","value":"bad password"}`)
		if out.OK || out.Kind != pdk.KindAuthFailed || !strings.Contains(out.Error, "bad password") {
			t.Fatalf("output = %+v, want auth_failed with the message", out)
		}
	})

	t.Run("timeout stops a spinning module", func(t *testing.T) {
		e := fresh()
		e.manifest.Limits.Timeout = 200 * time.Millisecond
		inst := e.instance(t)
		start := time.Now()
		_, err := inst.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"spin"}`)})
		if !errors.Is(err, runner.ErrTimeout) {
			t.Fatalf("spinning module returned %v, want runner.ErrTimeout", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("Handle took %v, want it stopped near the 200 ms timeout", d)
		}
		call(t, inst, `{"op":"echo"}`) // the next call gets a fresh instance
	})

	// The caller's deadline is not the module's time limit: the error says
	// who stopped the call, so a slow caller is not read as a slow module.
	t.Run("a caller's deadline is not the module's timeout", func(t *testing.T) {
		e := fresh()
		e.manifest.Limits.Timeout = time.Minute
		inst := e.instance(t)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, err := inst.Handle(ctx, pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"spin"}`)})
		if errors.Is(err, runner.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out after") {
			t.Fatalf("spinning module under its caller's deadline returned %v, want the caller's deadline", err)
		}
		call(t, inst, `{"op":"echo"}`)
	})

	t.Run("memory limit stops a greedy module", func(t *testing.T) {
		e := fresh()
		e.manifest.Limits.Memory = 32 << 20
		inst := e.instance(t)
		_, err := inst.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"alloc","mib":64}`)})
		if err == nil || errors.Is(err, runner.ErrTimeout) {
			t.Fatalf("allocating 64 MiB under a 32 MiB limit: %v, want a trap", err)
		}
		call(t, inst, `{"op":"echo"}`)
	})

	runCellTests(t, fresh)
	runNotifyTests(t, fresh)
	runEmitTests(t, fresh)
	runCallTests(t, fresh)
	runServeTests(t, fresh)
	runLifecycleTests(t, fresh)
}

// syncBuffer is a strings.Builder safe for concurrent log writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
