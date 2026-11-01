// Package extismrun runs ganglion modules through the Extism Go SDK. It is
// the second variant of the P0 spike for ADR-0011 and implements the same
// contract as runner.Load, so the conformance suite in runnertest covers both.
//
// Mapping of ganglion's manifest onto Extism:
//   - http.fetch → Extism allowed_hosts and its built-in HTTP function;
//   - config     → Extism config;
//   - log        → Extism log, forwarded to Deps.Logger;
//   - state.*, secret.get → host functions in the extism:host/user namespace;
//   - limits     → Extism max_pages and timeout_ms.
package extismrun

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

const (
	wasmPage        = 64 << 10
	maxHTTPResponse = 1 << 20
)

// compilationCache plays the same role as the one in package runner.
var compilationCache = wazero.NewCompilationCache()

func init() {
	// Extism drops plugin logs unless a global level is set.
	extism.SetLogLevel(extism.LogLevelInfo)
}

// Module is a compiled Extism plugin; each call runs in a fresh instance.
type Module struct {
	manifest runner.Manifest
	deps     runner.Deps
	compiled *extism.CompiledPlugin
	mu       sync.Mutex
}

// Load compiles a module for Extism.
func Load(ctx context.Context, wasm []byte, m runner.Manifest, deps runner.Deps) (*Module, error) {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	mod := &Module{manifest: m, deps: deps}
	em := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: wasm, Name: "main"}},
		AllowedHosts: m.Capabilities.HTTPFetch,
		Config:       m.Config,
		Memory: &extism.ManifestMemory{
			MaxPages:             uint32((m.Limits.Memory + wasmPage - 1) / wasmPage),
			MaxHttpResponseBytes: maxHTTPResponse,
		},
		Timeout: uint64(m.Limits.Timeout.Milliseconds()),
	}
	compiled, err := extism.NewCompiledPlugin(ctx, em, extism.PluginConfig{
		EnableWasi:    true,
		RuntimeConfig: wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithCompilationCache(compilationCache),
	}, mod.hostFunctions())
	if err != nil {
		return nil, fmt.Errorf("extismrun: compile %s: %w", m.Name, err)
	}
	mod.compiled = compiled
	return mod, nil
}

// Close frees the compiled plugin.
func (m *Module) Close(ctx context.Context) error {
	return m.compiled.Close(ctx)
}

// Handle runs one call in a fresh plugin instance.
func (m *Module) Handle(ctx context.Context, in pdk.Input) (pdk.Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	input, err := json.Marshal(in)
	if err != nil {
		return pdk.Output{}, err
	}
	plugin, err := m.compiled.Instance(ctx, extism.PluginInstanceConfig{
		ModuleConfig: wazero.NewModuleConfig().WithSysWalltime().WithSysNanotime(),
	})
	if err != nil {
		return pdk.Output{}, fmt.Errorf("extismrun: %s instantiate: %w", m.manifest.Name, err)
	}
	defer func() { _ = plugin.Close(context.Background()) }()
	plugin.SetLogger(func(_ extism.LogLevel, msg string) {
		m.deps.Logger.Info(msg, "module", m.manifest.Name)
	})

	rc, raw, err := plugin.CallWithContext(ctx, "handle", input)
	if err != nil {
		return pdk.Output{}, fmt.Errorf("extismrun: %s handle: %w", m.manifest.Name, err)
	}
	if rc != 0 {
		return pdk.Output{}, fmt.Errorf("extismrun: %s handle returned %d", m.manifest.Name, rc)
	}
	var out pdk.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return pdk.Output{}, fmt.Errorf("extismrun: %s returned bad output: %w", m.manifest.Name, err)
	}
	return out, nil
}

func (m *Module) hostFunctions() []extism.HostFunction {
	name := m.manifest.Name
	caps := m.manifest.Capabilities
	ptr := []extism.ValueType{extism.ValueTypePTR}

	// offer writes v into plugin memory and returns its offset, 0 if absent.
	offer := func(p *extism.CurrentPlugin, v string, ok bool) uint64 {
		if !ok {
			return 0
		}
		off, err := p.WriteString(v)
		if err != nil {
			panic(err)
		}
		return off
	}
	read := func(p *extism.CurrentPlugin, off uint64) string {
		s, err := p.ReadString(off)
		if err != nil {
			panic(err)
		}
		return s
	}

	stateGet := extism.NewHostFunctionWithStack("state_get",
		func(_ context.Context, p *extism.CurrentPlugin, stack []uint64) {
			if !caps.State {
				stack[0] = 0
				return
			}
			v, ok := m.deps.State.Get(name, read(p, stack[0]))
			stack[0] = offer(p, v, ok)
		}, ptr, ptr)

	statePut := extism.NewHostFunctionWithStack("state_put",
		func(_ context.Context, p *extism.CurrentPlugin, stack []uint64) {
			if !caps.State {
				m.deps.Logger.Warn("state write denied: no state capability", "module", name)
				return
			}
			m.deps.State.Put(name, read(p, stack[0]), read(p, stack[1]))
		}, []extism.ValueType{extism.ValueTypePTR, extism.ValueTypePTR}, nil)

	secretGet := extism.NewHostFunctionWithStack("secret_get",
		func(_ context.Context, p *extism.CurrentPlugin, stack []uint64) {
			key := read(p, stack[0])
			if !slices.Contains(caps.SecretGet, key) || m.deps.Secrets == nil {
				stack[0] = 0
				return
			}
			v, ok := m.deps.Secrets(key)
			stack[0] = offer(p, v, ok)
		}, ptr, ptr)

	fns := []extism.HostFunction{stateGet, statePut, secretGet}
	for i := range fns {
		fns[i].SetNamespace("extism:host/user")
	}
	return fns
}
