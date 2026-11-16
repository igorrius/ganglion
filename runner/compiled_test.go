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

// #219: closing a build's last load leaves no compiled code behind except
// the few recently loaded builds the runner keeps, so builds compiled for
// trials and checks and never installed do not pile up.
func TestClosedBuildsDoNotPileUp(t *testing.T) {
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := runner.Manifest{Name: "probe", Version: 1, Limits: runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second}}
	runner.ForgetKept()
	t.Cleanup(runner.ForgetKept)
	before := runner.CompiledModules()
	for i := range 3 { // six builds, more than the runner keeps
		mod, err := runner.Load(ctx, withNonce(probe, i), m, runner.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if err := mod.Close(ctx); err != nil {
			t.Fatal(err)
		}
		_ = mod.Close(ctx) // a second Close releases nothing more
		tool, err := runner.LoadTool(ctx, withNonce(probe, 100+i), []byte("name: probe\n"), runner.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if err := tool.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := runner.CompiledModules(), before+runner.KeptBuilds; got != want {
		t.Fatalf("after six builds loaded and closed: %d compiled, want %d (the kept ones)", got, want)
	}

	// A kept build loads again from the cache; one still loaded runs after
	// another load of it is closed.
	a, err := runner.Load(ctx, withNonce(probe, 102), m, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := runner.Load(ctx, withNonce(probe, 102), m, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.KeepsBuildOf(b) {
		t.Fatal("the kept reference is not the cache entry a load uses")
	}
	_ = a.Close(ctx)
	if _, err := b.Handle(ctx, pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"echo"}`)}); err != nil {
		t.Fatalf("the build still loaded no longer runs: %v", err)
	}
	_ = b.Close(ctx)
	if got, want := runner.CompiledModules(), before+runner.KeptBuilds; got != want {
		t.Fatalf("after reloading a kept build: %d compiled, want %d", got, want)
	}

	runner.ForgetKept()
	if got := runner.CompiledModules(); got != before {
		t.Fatalf("with nothing kept: %d compiled, want %d", got, before)
	}
}
