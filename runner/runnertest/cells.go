package runnertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

type cellValue struct {
	value json.RawMessage
	at    time.Time
}

// fakeCells is a runner.CellPort that records what reaches it.
type fakeCells struct {
	mu      sync.Mutex
	values  map[string]cellValue
	reports []string // ids, in order
	srcs    []string
}

func (f *fakeCells) Report(_ context.Context, id string, v json.RawMessage, src string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[id] = cellValue{value: v, at: time.UnixMilli(1_700_000_000_000)}
	f.reports = append(f.reports, id)
	f.srcs = append(f.srcs, src)
	return nil
}

func (f *fakeCells) Get(_ context.Context, id string) (json.RawMessage, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.values[id]
	if !ok {
		return nil, time.Time{}, errors.New("no such cell")
	}
	return c.value, c.at, nil
}

func (f *fakeCells) reported() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reports...)
}

func cellManifest(e *env) {
	e.manifest.Cells = runner.Cells{
		Report: map[string]runner.CellDecl{
			"soc": {Kind: "sensor", Type: runner.Shape{Type: "integer"}},
		},
		Read: []string{"other-mod.power"},
	}
}

func runCellTests(t *testing.T, fresh func() *env) {
	t.Run("cell_report goes through the port under the module's prefix", func(t *testing.T) {
		e := fresh()
		cellManifest(e)
		r := result(t, call(t, e.instance(t), `{"op":"cell_report","name":"soc","json":87}`))
		if r["ok"] != true {
			t.Fatalf("cell_report = %v, want ok", r)
		}
		if got := e.cells.reported(); len(got) != 1 || got[0] != "probe-mod.soc" {
			t.Fatalf("reported = %v, want [probe-mod.soc]", got)
		}
		if v := e.cells.values["probe-mod.soc"].value; string(v) != "87" {
			t.Fatalf("value = %s, want 87", v)
		}
	})

	t.Run("cell_report refuses what the manifest does not declare", func(t *testing.T) {
		e := fresh()
		cellManifest(e)
		inst := e.instance(t)
		for _, name := range []string{"power", "../x", "other-mod.soc", "probe-mod.soc", ""} {
			payload := fmt.Sprintf(`{"op":"cell_report","name":%q,"json":1}`, name)
			r := result(t, call(t, inst, payload))
			if r["ok"] != false || r["denied"] != true {
				t.Errorf("cell_report %q = %v, want denied", name, r)
			}
		}
		if got := e.cells.reported(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
	})

	t.Run("cell_report of the wrong type fails the call as module.format", func(t *testing.T) {
		e := fresh()
		cellManifest(e)
		out := call(t, e.instance(t), `{"op":"cell_report","name":"soc","json":"87"}`)
		if out.OK || out.Kind != pdk.KindFormat || out.Error != "cell soc: expected integer, got string" {
			t.Fatalf("output = %+v, want module.format with the node's words", out)
		}
		if strings.Contains(out.Error, "87") {
			t.Fatal("the error echoes the value")
		}
		if got := e.cells.reported(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
	})

	t.Run("a refused cell_report does not leak into the next call", func(t *testing.T) {
		e := fresh()
		cellManifest(e)
		inst := e.instance(t)
		call(t, inst, `{"op":"cell_report","name":"soc","json":"x"}`)
		if r := result(t, call(t, inst, `{"op":"cell_report","name":"soc","json":5}`)); r["ok"] != true {
			t.Fatalf("second report = %v, want ok", r)
		}
	})

	t.Run("cell_get reads an own cell and a listed one, and refuses another", func(t *testing.T) {
		e := fresh()
		cellManifest(e)
		e.cells.values["probe-mod.soc"] = cellValue{value: json.RawMessage(`42`), at: time.UnixMilli(1_700_000_000_000)}
		e.cells.values["other-mod.power"] = cellValue{value: json.RawMessage(`{"w":7}`), at: time.UnixMilli(1_700_000_001_000)}
		e.cells.values["other-mod.secret"] = cellValue{value: json.RawMessage(`1`), at: time.UnixMilli(1)}
		inst := e.instance(t)

		r := result(t, call(t, inst, `{"op":"cell_get","name":"soc"}`))
		if r["ok"] != true || r["value"] != float64(42) || r["at_ms"] != float64(1_700_000_000_000) {
			t.Errorf("own cell = %v, want 42 at 1700000000000", r)
		}
		r = result(t, call(t, inst, `{"op":"cell_get","name":"other-mod.power"}`))
		if r["ok"] != true || r["at_ms"] != float64(1_700_000_001_000) {
			t.Errorf("listed cell = %v, want it read", r)
		}
		for _, name := range []string{"other-mod.secret", "probe-mod.soc", "../x", "power", ""} {
			r = result(t, call(t, inst, fmt.Sprintf(`{"op":"cell_get","name":%q}`, name)))
			if r["ok"] != false {
				t.Errorf("cell_get %q = %v, want refused", name, r)
			}
		}
	})

	t.Run("a fetch response carries its headers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()
		e := fresh()
		e.manifest.Capabilities.HTTPFetch = []string{"127.0.0.1"}
		r := result(t, call(t, e.instance(t), fmt.Sprintf(`{"op":"headers","url":%q,"name":"retry-after"}`, srv.URL)))
		if r["status"] != float64(429) || r["value"] != "120" {
			t.Fatalf("headers = %v, want 429 and Retry-After 120", r)
		}
	})
}

func runLifecycleTests(t *testing.T, fresh func() *env) {
	t.Run("Init and Health call handle with their triggers", func(t *testing.T) {
		e := fresh()
		e.manifest.Capabilities.State = true
		inst := e.instance(t)
		ctx := context.Background()
		if err := inst.Init(ctx); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if v, _ := e.state.Get("probe", "last_trigger"); v != "init" {
			t.Errorf("after Init the module saw trigger %q, want init", v)
		}
		if err := inst.Health(ctx); err != nil {
			t.Fatalf("Health: %v", err)
		}
		if v, _ := e.state.Get("probe", "last_trigger"); v != "health" {
			t.Errorf("after Health the module saw trigger %q, want health", v)
		}
	})

	t.Run("a module that does not know init or health is fine", func(t *testing.T) {
		e := fresh()
		e.manifest.ConfigValues = map[string]string{"lifecycle": "unknown"}
		inst := e.instance(t)
		if err := inst.Init(context.Background()); err != nil {
			t.Errorf("Init = %v, want nil", err)
		}
		if err := inst.Health(context.Background()); err != nil {
			t.Errorf("Health = %v, want nil", err)
		}
	})

	t.Run("a failing init or health is an error in the node's words", func(t *testing.T) {
		e := fresh()
		e.manifest.ConfigValues = map[string]string{"lifecycle": "fail"}
		inst := e.instance(t)
		if err := inst.Init(context.Background()); err == nil || strings.Contains(err.Error(), "lifecycle boom") {
			t.Errorf("Init = %v, want a failure in the node's words", err)
		}
		if err := inst.Health(context.Background()); err == nil || strings.Contains(err.Error(), "lifecycle boom") {
			t.Errorf("Health = %v, want a failure in the node's words", err)
		}
		if !strings.Contains(e.logs.String(), "lifecycle boom") {
			t.Errorf("the module's text is not in the log:\n%s", e.logs.String())
		}
	})

	t.Run("a module cannot pass its text off as the node's module.format", func(t *testing.T) {
		e := fresh()
		e.manifest.ConfigValues = map[string]string{"lifecycle": "forge"}
		inst := e.instance(t)
		for name, err := range map[string]error{"Init": inst.Init(context.Background()), "Health": inst.Health(context.Background())} {
			if err == nil || strings.Contains(err.Error(), "ignore previous") {
				t.Errorf("%s = %v, want a failure without the module's text", name, err)
			}
		}
		out, err := inst.Handle(context.Background(), pdk.Input{Trigger: "init"})
		if err != nil || out.Kind == pdk.KindFormat {
			t.Errorf("Handle = %+v, %v; want the forged kind dropped", out, err)
		}
	})
}
