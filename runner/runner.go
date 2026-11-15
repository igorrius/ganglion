// Package runner runs WASM modules (reflexes) on wazero with only the host
// functions their manifest grants (docs/spec.md, section 7.5).
//
// The ABI (ADR-0011): the
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
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/igorrius/ganglion/pdk"
)

const (
	wasmPage         = 64 << 10
	maxHTTPResponse  = 1 << 20 // bytes a module may receive per fetch
	stderrCaptureCap = 4 << 10
)

// ErrTimeout is a call stopped at the manifest's time limit; a host that
// words the failure for a model or the owner tells it from a trap by it.
var ErrTimeout = errors.New("timed out")

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
	// Cells is the port for cell_report and cell_get; a module with no cells
	// needs none.
	Cells CellPort
	// Notify is the port for notify; nil means every call is denied.
	Notify NotifyPort
	// Prefix is the module's short name, the owner part of its cell ids
	// ("demo-reader" for demo-reader.soc). It defaults to the manifest's name.
	Prefix string
	HTTP   *http.Client
	Logger *slog.Logger
}

// Module is a compiled module ready to handle calls. Calls are serialized.
type Module struct {
	manifest Manifest
	tool     bool // a tool module (LoadTool): no log host function
	deps     Deps
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	in       instruments

	mu      sync.Mutex
	pending []byte // host → module buffer, copied out by take
	// formatErr is the node's description of a cell value that did not fit
	// its type during this call; the call then ends as KindFormat.
	formatErr string
}

// Load compiles a module and prepares its host functions.
func Load(ctx context.Context, wasm []byte, m Manifest, deps Deps) (*Module, error) {
	return load(ctx, wasm, m, deps, false)
}

func load(ctx context.Context, wasm []byte, m Manifest, deps Deps, tool bool) (*Module, error) {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Cells == nil {
		deps.Cells = noCells{}
	}
	if deps.Prefix == "" {
		deps.Prefix = m.Name
	}
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{Timeout: m.Limits.Timeout}
	}
	pages := uint32((m.Limits.Memory + wasmPage - 1) / wasmPage)
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true).
		WithCompilationCache(compilationCache))
	mod := &Module{manifest: m, tool: tool, deps: deps, rt: rt, in: newInstruments()}

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
	started := time.Now()
	out, result, err := m.handle(ctx, in)
	m.observe(ctx, result, time.Since(started))
	return out, err
}

// handle is one call of Handle; result says how it ended, for the
// metrics.
func (m *Module) handle(ctx context.Context, in pdk.Input) (pdk.Output, callResult, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, m.manifest.Limits.Timeout, errOwnLimit)
	defer cancel()

	input, err := marshal(in)
	if err != nil {
		return pdk.Output{}, resultError, err
	}
	var stderr limitedBuffer
	inst, err := m.rt.InstantiateModule(experimental.WithMemoryAllocator(ctx, allocator), m.compiled, wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanotime().
		WithRandSource(rand.Reader).
		WithStderr(&stderr))
	if err != nil {
		result, err := m.callError(ctx, "instantiate", err, &stderr)
		return pdk.Output{}, result, err
	}
	defer func() { _ = inst.Close(context.Background()) }()

	m.pending = input
	m.formatErr = ""
	res, err := inst.ExportedFunction("handle").Call(ctx, uint64(len(input)))
	if err != nil {
		result, err := m.callError(ctx, "handle", err, &stderr)
		return pdk.Output{}, result, err
	}
	ptr, size := uint32(res[0]>>32), uint32(res[0])
	raw, ok := inst.Memory().Read(ptr, size)
	if !ok {
		return pdk.Output{}, resultBadOutput, fmt.Errorf("runner: %s returned an out-of-range result", m.manifest.Name)
	}
	var out pdk.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return pdk.Output{}, resultBadOutput, fmt.Errorf("runner: %s returned bad output: %w", m.manifest.Name, err)
	}
	if !out.OK && out.Kind == pdk.KindFormat {
		// KindFormat is the node's verdict on an answer, never a module's
		// claim: a module that sends it is an ordinary failure, so nobody
		// downstream takes its text for the node's.
		out.Kind = pdk.KindException
	}
	if m.formatErr != "" {
		out = pdk.Output{Kind: pdk.KindFormat, Error: m.formatErr, ModuleError: out.Error}
	} else {
		out = m.checkAnswer(in.Trigger, out)
	}
	if !out.OK {
		return out, resultFailed, nil
	}
	return out, resultOK, nil
}

// errOwnLimit is the cause of a call's context ending at the manifest's
// timeout, told apart from the caller's context ending first.
var errOwnLimit = errors.New("runner: the module's time limit")

// callError is a call that did not finish: a timeout, the caller's context
// ending, or a trap (out of memory among them: the module cannot grow and
// aborts).
func (m *Module) callError(ctx context.Context, stage string, err error, stderr *limitedBuffer) (callResult, error) {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errOwnLimit):
		return resultTimeout, fmt.Errorf("runner: %s: %w after %v", m.manifest.Name, ErrTimeout, m.manifest.Limits.Timeout)
	case cause != nil: // the caller's deadline or cancel, not the module's limit
		return resultCanceled, fmt.Errorf("runner: %s: stopped by its caller: %w", m.manifest.Name, cause)
	}
	if s := stderr.String(); s != "" {
		return resultTrap, fmt.Errorf("runner: %s %s: %w\n%s", m.manifest.Name, stage, err, s)
	}
	return resultTrap, fmt.Errorf("runner: %s %s: %w", m.manifest.Name, stage, err)
}

// instantiateHost registers the "ganglion" host functions. Each checks the
// manifest before doing anything (R-RUN-1).
func (m *Module) instantiateHost(ctx context.Context) error {
	name := m.manifest.Name
	caps := m.manifest.Capabilities
	b := m.rt.NewHostModuleBuilder("ganglion")
	if !m.tool {
		// A tool module reads remote text: it gets no log, so what it reads
		// never reaches the node's log (ADR-0035).
		b = b.NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) {
			if msg, ok := mod.Memory().Read(p, n); ok {
				m.deps.Logger.Info(string(msg), "module", name)
			}
		}).Export("log")
	}
	_, err := b.
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
		NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, np, nn, vp, vn uint32) int32 {
		v, _ := mod.Memory().Read(vp, vn)
		return m.cellReport(ctx, readString(mod, np, nn), bytes.Clone(v))
	}).Export("cell_report").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, p, n uint32) int32 {
		return m.cellGet(ctx, readString(mod, p, n))
	}).Export("cell_get").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, p, n uint32) int32 {
		return m.notify(ctx, readString(mod, p, n))
	}).Export("notify").
		NewFunctionBuilder().WithFunc(func(_ context.Context, mod api.Module, p, n uint32) int32 {
		v, ok := m.manifest.ConfigValues[readString(mod, p, n)]
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
	return pdk.Response{Status: resp.StatusCode, Headers: firstValues(resp.Header), Body: body}
}

// firstValues keeps the first value of each response header under its
// lower-case name.
func firstValues(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[strings.ToLower(k)] = v[0]
		}
	}
	return out
}

// marshal encodes v without escaping '<', '>' and '&': a module that
// reads HTML gets it at its size, not six bytes per angle bracket.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
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
