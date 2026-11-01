// Package runner runs WASM modules (reflexes) on wazero with only the host
// functions their manifest grants (docs/spec.md, section 7.5).
//
// P0 status: this is the "own ABI" variant of the spike for ADR-0011. The
// module exports handle(inLen u32) u64 and imports host functions from the
// "ganglion" module; data crosses the boundary as JSON (see package pdk).
// Each call gets a fresh module instance, so a trap or timeout never leaves
// a broken instance behind (R-RUN-2); persistent data lives in State.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/igorrius/ganglion/pdk"
)

const (
	wasmPage         = 64 << 10
	maxHTTPResponse  = 1 << 20 // bytes a module may receive per fetch
	stderrCaptureCap = 4 << 10
)

// compilationCache is shared by every module in the process, so loading the
// same module again (a new version rollout, a restart of a role) skips the
// expensive compilation step.
var compilationCache = wazero.NewCompilationCache()

// Deps are the runner's side of the host functions.
type Deps struct {
	// Secrets looks up a secret by name. Only names the manifest grants are
	// ever asked for.
	Secrets func(name string) (string, bool)
	State   State
	HTTP    *http.Client
	Logger  *slog.Logger
}

// Module is a compiled module ready to handle calls. Calls are serialized.
type Module struct {
	manifest Manifest
	deps     Deps
	rt       wazero.Runtime
	compiled wazero.CompiledModule

	mu      sync.Mutex
	pending []byte // host → module buffer, copied out by take
}

// Load compiles a module and prepares its host functions.
func Load(ctx context.Context, wasm []byte, m Manifest, deps Deps) (*Module, error) {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{Timeout: m.Limits.Timeout}
	}
	pages := uint32((m.Limits.Memory + wasmPage - 1) / wasmPage)
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true).
		WithCompilationCache(compilationCache))
	mod := &Module{manifest: m, deps: deps, rt: rt}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("runner: wasi: %w", err)
	}
	if err := mod.instantiateHost(ctx); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("runner: host module: %w", err)
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("runner: compile %s: %w", m.Name, err)
	}
	if compiled.ExportedFunctions()["handle"] == nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("runner: module %s does not export handle", m.Name)
	}
	mod.compiled = compiled
	return mod, nil
}

// Close frees the module and its runtime.
func (m *Module) Close(ctx context.Context) error {
	return m.rt.Close(ctx)
}

// Handle runs one call of the module within its time and memory limits.
// An error means the call itself failed (trap, timeout, out of memory); a
// module that ran and reported a failure returns an Output with OK false.
func (m *Module) Handle(ctx context.Context, in pdk.Input) (pdk.Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, m.manifest.Limits.Timeout)
	defer cancel()

	input, err := json.Marshal(in)
	if err != nil {
		return pdk.Output{}, err
	}
	var stderr limitedBuffer
	inst, err := m.rt.InstantiateModule(ctx, m.compiled, wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanotime().
		WithRandSource(rand.Reader).
		WithStderr(&stderr))
	if err != nil {
		return pdk.Output{}, m.callError(ctx, "instantiate", err, &stderr)
	}
	defer func() { _ = inst.Close(context.Background()) }()

	m.pending = input
	res, err := inst.ExportedFunction("handle").Call(ctx, uint64(len(input)))
	if err != nil {
		return pdk.Output{}, m.callError(ctx, "handle", err, &stderr)
	}
	ptr, size := uint32(res[0]>>32), uint32(res[0])
	raw, ok := inst.Memory().Read(ptr, size)
	if !ok {
		return pdk.Output{}, fmt.Errorf("runner: %s returned an out-of-range result", m.manifest.Name)
	}
	var out pdk.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return pdk.Output{}, fmt.Errorf("runner: %s returned bad output: %w", m.manifest.Name, err)
	}
	return out, nil
}

func (m *Module) callError(ctx context.Context, stage string, err error, stderr *limitedBuffer) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("runner: %s: timed out after %v", m.manifest.Name, m.manifest.Limits.Timeout)
	}
	if s := stderr.String(); s != "" {
		return fmt.Errorf("runner: %s %s: %w\n%s", m.manifest.Name, stage, err, s)
	}
	return fmt.Errorf("runner: %s %s: %w", m.manifest.Name, stage, err)
}

// instantiateHost registers the "ganglion" host functions. Each checks the
// manifest before doing anything (R-RUN-1).
func (m *Module) instantiateHost(ctx context.Context) error {
	name := m.manifest.Name
	caps := m.manifest.Capabilities
	_, err := m.rt.NewHostModuleBuilder("ganglion").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) {
		if b, ok := mod.Memory().Read(p, n); ok {
			m.deps.Logger.Info(string(b), "module", name)
		}
	}).Export("log").
		NewFunctionBuilder().WithFunc(func() int64 {
		return time.Now().UnixMilli()
	}).Export("now_ms").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p uint32) {
		mod.Memory().Write(p, m.pending)
		m.pending = nil
	}).Export("take").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, p, n uint32) uint32 {
		b, _ := mod.Memory().Read(p, n)
		resp := m.fetch(ctx, b)
		m.pending, _ = json.Marshal(resp)
		return uint32(len(m.pending))
	}).Export("http_fetch").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) int32 {
		if !caps.State {
			return -1
		}
		return m.offer(m.deps.State.Get(name, readString(mod, p, n)))
	}).Export("state_get").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, kp, kn, vp, vn uint32) {
		if !caps.State {
			m.deps.Logger.Warn("state write denied: no state capability", "module", name)
			return
		}
		m.deps.State.Put(name, readString(mod, kp, kn), readString(mod, vp, vn))
	}).Export("state_put").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) int32 {
		key := readString(mod, p, n)
		if !slices.Contains(caps.SecretGet, key) || m.deps.Secrets == nil {
			return -1
		}
		return m.offer(m.deps.Secrets(key))
	}).Export("secret_get").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) int32 {
		v, ok := m.manifest.Config[readString(mod, p, n)]
		return m.offer(v, ok)
	}).Export("config_get").
		Instantiate(ctx)
	return err
}

// offer leaves a value for take and returns its length, or -1 if absent.
func (m *Module) offer(v string, ok bool) int32 {
	if !ok {
		return -1
	}
	m.pending = []byte(v)
	return int32(len(m.pending))
}

func (m *Module) fetch(ctx context.Context, raw []byte) pdk.Response {
	var req pdk.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return pdk.Response{Error: "bad request: " + err.Error()}
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return pdk.Response{Error: "bad URL " + req.URL}
	}
	if !slices.Contains(m.manifest.Capabilities.HTTPFetch, u.Hostname()) {
		m.deps.Logger.Warn("http.fetch denied", "module", m.manifest.Name, "host", u.Hostname())
		return pdk.Response{Error: "host " + u.Hostname() + " is not allowed by the manifest"}
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return pdk.Response{Error: err.Error()}
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := m.deps.HTTP.Do(hr)
	if err != nil {
		return pdk.Response{Error: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponse+1))
	if err != nil {
		return pdk.Response{Error: err.Error()}
	}
	if len(body) > maxHTTPResponse {
		return pdk.Response{Error: "response body exceeds 1 MiB"}
	}
	return pdk.Response{Status: resp.StatusCode, Body: body}
}

func readString(mod api.Module, p, n uint32) string {
	b, _ := mod.Memory().Read(p, n)
	return string(b)
}

// limitedBuffer keeps the first few KiB a module writes to stderr (a Go
// panic message, for example).
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := stderrCaptureCap - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
