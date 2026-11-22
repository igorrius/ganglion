package modhost_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
)

// withLifecycle is the probe whose init and health answer as mode says.
func withLifecycle(mode string) string {
	return strings.Replace(probeManifest, "config:\n", "config:\n  lifecycle: {type: string, default: "+mode+"}\n", 1)
}

// build is the probe as a build of local/probe from manifest.
func build(t *testing.T, manifest string) modstore.Build {
	t.Helper()
	a := active(t, "local/probe", manifest)
	return modstore.Build{Module: a.Module, Wasm: a.Wasm, Manifest: a.Manifest}
}

// R-RUN-9: a build that loads and whose health passes is fine, and the check
// changes nothing: no cell, no job, no tool, no state.
func TestCheckPassesAHealthyBuildAndChangesNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.host.Check(context.Background(), build(t, withLifecycle("ok")), nil); err != nil {
		t.Fatal(err)
	}
	if cs, _ := e.cells.List(context.Background()); len(cs) != 0 {
		t.Fatalf("cells %+v", cs)
	}
	if len(e.jobs.all()) != 0 || len(e.host.Handlers()) != 0 || len(e.store.counts) != 0 {
		t.Fatal("the check left something behind")
	}
}

// A build that does not load, and one whose health fails, are refused in the
// node's words: the module's text never reaches the error.
func TestCheckRefusesWhatDoesNotLoadOrIsUnhealthy(t *testing.T) {
	e := newEnv(t)
	broken := build(t, probeManifest)
	broken.Wasm = append([]byte("\x00asm\x01\x00\x00\x00"), []byte(hostile)...)
	for name, c := range map[string]struct {
		b    modstore.Build
		want string
	}{
		"not wasm": {broken, "it does not load on this node"},
		"manifest": {modstore.Build{Module: "local/probe", Wasm: broken.Wasm, Manifest: []byte("name: probe\nbogus: 1\n")}, "its manifest does not load"},
		"health":   {build(t, withLifecycle("health_auth")), "its health check failed (auth_failed)"},
		"fails":    {build(t, withLifecycle("fail")), "its health check failed (module.exception)"},
		"forges":   {build(t, withLifecycle("forge")), "its health check failed (module.exception)"},
	} {
		t.Run(name, func(t *testing.T) {
			err := e.host.Check(context.Background(), c.b, nil)
			if !errors.Is(err, modhost.ErrCheckFailed) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if s := strings.ToLower(err.Error()); strings.Contains(s, "ignore") || strings.Contains(s, "boom") {
				t.Fatalf("the module's text in %q", err)
			}
		})
	}
}

// The check reads the config the module would run with: a key's value in the
// settings line, not only its default.
func TestCheckUsesTheModulesSettings(t *testing.T) {
	e := newEnv(t)
	err := e.host.Check(context.Background(), build(t, withLifecycle("ok")), map[string]any{"lifecycle": "health_auth"})
	if !errors.Is(err, modhost.ErrCheckFailed) {
		t.Fatalf("err %v", err)
	}
}

// Ruling 3 of M4b: the card runs the health of the running instance and shows
// ok or the kind; a module that does not run has none.
func TestTheCardShowsHealth(t *testing.T) {
	ctx := context.Background()
	for mode, want := range map[string]modstore.Health{"ok": modstore.HealthOK, "health_auth": "auth_failed", "unknown": modstore.HealthOK} {
		e := newEnv(t, active(t, "local/probe", withLifecycle(mode)))
		e.start(t)
		c, err := e.host.Card(ctx, "probe")
		if err != nil || c.Health != want {
			t.Fatalf("%s: health %q, %v; want %q", mode, c.Health, err, want)
		}
	}
	e := newEnv(t, active(t, "local/probe", withLifecycle("fail")))
	e.start(t)
	if c, err := e.host.Card(ctx, "probe"); err != nil || c.Health != "" {
		t.Fatalf("a stopped module: health %q, %v", c.Health, err)
	}
}

// A build's check is no run: its health's emit answers "not here" and
// nothing is published (R-BUS-6).
func TestABuildsCheckCannotEmit(t *testing.T) {
	e := newEnv(t)
	m := strings.Replace(probeManifest, "config:\n", "config:\n  lifecycle: {type: string, default: emit}\n", 1) + "ports:\n  emits: {x: number}\n"
	if err := e.host.Check(context.Background(), build(t, m), nil); err != nil {
		t.Fatalf("check: %v\n%s", err, e.logText())
	}
}

// A build's check calls nobody either: its health's call answers "not here"
// and nothing is sent (R-BUS-6).
func TestABuildsCheckCannotCall(t *testing.T) {
	e := newEnv(t)
	m := strings.Replace(probeManifest, "config:\n", "config:\n  lifecycle: {type: string, default: call}\n", 1) + "ports:\n  calls: {x: {in: number, out: number}}\n"
	if err := e.host.Check(context.Background(), build(t, m), nil); err != nil {
		t.Fatalf("check: %v\n%s", err, e.logText())
	}
}
