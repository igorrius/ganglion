package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/igorrius/ganglion/pdk"
)

// ErrCapabilities is a manifest that grants a pure tool module a host
// function: such a module takes input and returns output, nothing else
// (ADR-0035).
var ErrCapabilities = errors.New("runner: a tool module's manifest must grant no capabilities")

// ModuleError is a call the module itself reported as failed (Output with
// OK false). Msg is the module's own text: a caller that shows errors to
// a model words its own message instead.
type ModuleError struct {
	Module string
	Kind   string
	Msg    string
}

func (e *ModuleError) Error() string {
	return fmt.Sprintf("runner: %s failed (%s): %s", e.Module, e.Kind, e.Msg)
}

// ToolModule is a module used as a pure function by the core: JSON in,
// JSON out, no host functions granted (the "module as a tool" path of
// spec 7.5, pulled forward from M4 for html2md).
type ToolModule struct {
	mod *Module
}

// LoadTool parses the manifest, refuses one that grants any capability
// (ErrCapabilities), and compiles the module. A tool module gets no log
// host function: a module that imports one fails to instantiate.
func LoadTool(ctx context.Context, wasm, manifest []byte, deps Deps) (*ToolModule, error) {
	m, err := toolManifest(manifest)
	if err != nil {
		return nil, err
	}
	mod, err := load(ctx, wasm, m, deps, true)
	if err != nil {
		return nil, err
	}
	return &ToolModule{mod: mod}, nil
}

// toolManifest parses a tool module's manifest and refuses one that
// grants anything.
func toolManifest(src []byte) (Manifest, error) {
	m, err := ParseManifest(src)
	if err != nil {
		return Manifest{}, fmt.Errorf("runner: tool module: %w", err)
	}
	if c := m.Capabilities; len(c.HTTPFetch) > 0 || len(c.SecretGet) > 0 || c.State ||
		c.Notify != nil || len(m.Listens()) > 0 || m.Ports.Any() {
		return Manifest{}, fmt.Errorf("%w (%s)", ErrCapabilities, m.Name)
	}
	return m, nil
}

// Call runs the module once with trigger "tool" and payload, and returns
// its result. A trap, timeout or bad output is an error from the runner;
// a failure the module reports is a *ModuleError.
func (t *ToolModule) Call(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	out, err := t.mod.Handle(ctx, pdk.Input{Trigger: "tool", Payload: payload})
	if err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, &ModuleError{Module: t.mod.manifest.Name, Kind: out.Kind, Msg: out.Error}
	}
	return out.Result, nil
}

// Shutdown frees the module.
func (t *ToolModule) Shutdown(ctx context.Context) error {
	return t.mod.Close(ctx)
}

// LazyTool is a tool module compiled on its first call, so a node that
// never needs it does not pay for its compiled code (spec principle 8):
// html2md is needed only when someone reads an HTML page. The manifest
// is checked at once.
type LazyTool struct {
	wasm     []byte
	manifest []byte
	deps     Deps

	mu  sync.Mutex
	mod *ToolModule
}

// NewLazyTool checks the manifest as LoadTool does (ErrCapabilities)
// and leaves compiling to the first Call.
func NewLazyTool(wasm, manifest []byte, deps Deps) (*LazyTool, error) {
	if _, err := toolManifest(manifest); err != nil {
		return nil, err
	}
	return &LazyTool{wasm: wasm, manifest: manifest, deps: deps}, nil
}

// Call compiles the module if this is the first call (a failed compile
// is tried again on the next one) and runs it.
func (l *LazyTool) Call(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	l.mu.Lock()
	if l.mod == nil {
		mod, err := LoadTool(ctx, l.wasm, l.manifest, l.deps)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		l.mod = mod
	}
	mod := l.mod
	l.mu.Unlock()
	return mod.Call(ctx, payload)
}

// Loaded reports whether the module has been compiled.
func (l *LazyTool) Loaded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mod != nil
}

// Shutdown frees the module if it was loaded.
func (l *LazyTool) Shutdown(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mod == nil {
		return nil
	}
	return l.mod.Shutdown(ctx)
}
