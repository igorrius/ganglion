package runnertest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// sentEvent is one call that reached the fake port.
type sentEvent struct {
	port    string
	payload string
}

// fakeEmit is a runner.EmitPort that records what reaches it and answers
// with a configured code.
type fakeEmit struct {
	mu      sync.Mutex
	code    runner.EmitCode
	events  []sentEvent
	refused []runner.EmitCode
}

func (f *fakeEmit) Emit(_ context.Context, port string, payload json.RawMessage) runner.EmitCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, sentEvent{port, string(payload)})
	return f.code
}

func (f *fakeEmit) Refused(_ context.Context, code runner.EmitCode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused = append(f.refused, code)
}

func (f *fakeEmit) sent() []sentEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentEvent(nil), f.events...)
}

func (f *fakeEmit) refusals() []runner.EmitCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.EmitCode(nil), f.refused...)
}

func runEmitTests(t *testing.T, fresh func() *env) {
	ports := func(e *env) {
		e.manifest.Ports.Emits = map[string]runner.Shape{
			"reading": {Type: "integer"},
			"label":   {Type: "string"},
		}
	}

	t.Run("emit reaches the port with the port's name and the payload", func(t *testing.T) {
		e := fresh()
		ports(e)
		r := result(t, call(t, e.instance(t), `{"op":"emit","name":"reading","json":87}`))
		if r["err"] != "" {
			t.Fatalf("emit = %v, want sent", r)
		}
		if got := e.emit.sent(); len(got) != 1 || got[0] != (sentEvent{"reading", "87"}) {
			t.Fatalf("port saw %v, want reading 87", got)
		}
	})

	t.Run("emit to a port the manifest does not declare is denied, notify included", func(t *testing.T) {
		e := fresh()
		ports(e)
		e.manifest.Capabilities.Notify = &runner.Notify{PerDay: 5}
		inst := e.instance(t)
		for _, name := range []string{"power", "notify", "", "../x"} {
			r := result(t, call(t, inst, `{"op":"emit","name":"`+name+`","json":1}`))
			if r["err"] != "denied" {
				t.Errorf("emit %q = %v, want denied", name, r)
			}
		}
		if got := e.emit.sent(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
		if got := e.emit.refusals(); len(got) != 4 || got[0] != runner.EmitDenied {
			t.Fatalf("refusals = %v, want four denied", got)
		}
	})

	t.Run("a payload that breaks the shape is not sent and fails the call as module.format", func(t *testing.T) {
		e := fresh()
		ports(e)
		out := call(t, e.instance(t), `{"op":"emit","name":"reading","json":"87"}`)
		if out.OK || out.Kind != pdk.KindFormat || out.Error != "port reading: expected integer, got string" {
			t.Fatalf("output = %+v, want module.format with the node's words", out)
		}
		if strings.Contains(out.Error, "87") {
			t.Fatal("the error echoes the value")
		}
		if got := e.emit.sent(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
		if got := e.emit.refusals(); len(got) != 1 || got[0] != runner.EmitFormat {
			t.Fatalf("refusals = %v, want one format", got)
		}
	})

	t.Run("a payload over 64 KiB is refused as failed and not sent", func(t *testing.T) {
		e := fresh()
		ports(e)
		big := `"` + strings.Repeat("x", runner.MaxEmitPayload) + `"`
		r := result(t, call(t, e.instance(t), `{"op":"emit","name":"label","json":`+big+`}`))
		if r["err"] != "failed" {
			t.Fatalf("emit = %v, want failed", r)
		}
		if got := e.emit.sent(); len(got) != 0 {
			t.Fatalf("the port saw %d events, want none", len(got))
		}
	})

	t.Run("a run sends at most 100 events, and the next call starts afresh", func(t *testing.T) {
		e := fresh()
		ports(e)
		inst := e.instance(t)
		r := result(t, call(t, inst, `{"op":"emit_many","name":"reading","json":1,"mib":101}`))
		if r["err"] != "failed" || r["sent"] != float64(runner.MaxEmitsPerRun) {
			t.Fatalf("emit_many = %v, want failed after 100", r)
		}
		if got := e.emit.sent(); len(got) != runner.MaxEmitsPerRun {
			t.Fatalf("the port saw %d events, want 100", len(got))
		}
		if r := result(t, call(t, inst, `{"op":"emit","name":"reading","json":1}`)); r["err"] != "" {
			t.Fatalf("emit in the next call = %v, want sent", r)
		}
	})

	for name, tc := range map[string]struct {
		code runner.EmitCode
		want string
	}{
		"not here": {runner.EmitNotHere, "not_here"},
		"failed":   {runner.EmitFailed, "failed"},
	} {
		t.Run("the guest maps the port's code: "+name, func(t *testing.T) {
			e := fresh()
			ports(e)
			e.emit.code = tc.code
			r := result(t, call(t, e.instance(t), `{"op":"emit","name":"reading","json":1}`))
			if r["err"] != tc.want {
				t.Fatalf("emit = %v, want err %q", r, tc.want)
			}
		})
	}
}
