package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
// (ErrCapabilities), and compiles the module.
func LoadTool(ctx context.Context, wasm, manifest []byte, deps Deps) (*ToolModule, error) {
	m, err := ParseManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("runner: tool module: %w", err)
	}
	if c := m.Capabilities; len(c.HTTPFetch) > 0 || len(c.SecretGet) > 0 || len(c.EventEmit) > 0 || c.State {
		return nil, fmt.Errorf("%w (%s)", ErrCapabilities, m.Name)
	}
	mod, err := Load(ctx, wasm, m, deps)
	if err != nil {
		return nil, err
	}
	return &ToolModule{mod: mod}, nil
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
