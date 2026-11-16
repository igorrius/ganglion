package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

// withNonce appends a custom section to wasm, so each copy is a different
// build to the compilation cache: a trial compiles builds that are never
// installed, each one new.
func withNonce(wasm []byte, i int) []byte {
	name := "nonce"
	payload := fmt.Appendf(nil, "%d", i)
	body := append([]byte{byte(len(name))}, name...)
	body = append(body, payload...)
	out := append([]byte(nil), wasm...)
	out = append(out, 0, byte(len(body))) // custom section id, size (< 128)
	return append(out, body...)
}

// #219: loading a build and closing it leaves no compiled module behind,
// whether it loaded as a module or as a tool, or failed to load.
func TestCloseFreesTheCompiledModule(t *testing.T) {
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := runner.Manifest{Name: "probe", Version: 1, Limits: runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second}}
	before := runner.CompiledModules()
	for i := range 5 {
		mod, err := runner.Load(ctx, withNonce(probe, i), m, runner.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if err := mod.Close(ctx); err != nil {
			t.Fatal(err)
		}
		tool, err := runner.LoadTool(ctx, withNonce(probe, 100+i), []byte("name: probe\n"), runner.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if err := tool.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := runner.CompiledModules(); got != before {
		t.Fatalf("compiled modules in the cache: %d before, %d after 10 loads and closes", before, got)
	}

	// The same build loaded twice stays compiled until the last close.
	a, err := runner.Load(ctx, withNonce(probe, 200), m, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	one := runner.CompiledModules() // the build and a runtime's own host modules
	b, err := runner.Load(ctx, withNonce(probe, 200), m, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Close(ctx)
	if got := runner.CompiledModules(); got != one {
		t.Fatalf("after closing one of two loads of a build: %d compiled, want %d", got, one)
	}
	if _, err := b.Handle(ctx, pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"echo"}`)}); err != nil {
		t.Fatalf("the build still loaded no longer runs: %v", err)
	}
	_ = b.Close(ctx)
	if got := runner.CompiledModules(); got != before {
		t.Fatalf("after closing both loads: %d compiled, want %d", got, before)
	}
}
