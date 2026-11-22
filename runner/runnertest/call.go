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

// sentCall is one call that reached the fake port.
type sentCall struct {
	port    string
	request string
}

// fakeCall is a runner.CallPort that records what reaches it and answers
// with a configured answer and code.
type fakeCall struct {
	mu      sync.Mutex
	code    runner.CallCode
	answer  string
	calls   []sentCall
	settled []runner.CallCode
}

func (f *fakeCall) Call(_ context.Context, port string, request json.RawMessage) (json.RawMessage, runner.CallCode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sentCall{port, string(request)})
	if f.code != runner.CallAnswered {
		return nil, f.code
	}
	return json.RawMessage(f.answer), runner.CallAnswered
}

func (f *fakeCall) Settled(_ context.Context, code runner.CallCode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled = append(f.settled, code)
}

func (f *fakeCall) sent() []sentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentCall(nil), f.calls...)
}

func (f *fakeCall) codes() []runner.CallCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.CallCode(nil), f.settled...)
}

func runCallTests(t *testing.T, fresh func() *env) {
	ports := func(e *env) {
		e.manifest.Ports.Calls = map[string]runner.PortCall{
			"ask":  {In: runner.Shape{Type: "integer"}, Out: runner.Shape{Type: "string"}},
			"text": {In: runner.Shape{Type: "string"}, Out: runner.Shape{Type: "string"}},
		}
		e.call.answer = `"yes"`
	}

	t.Run("call reaches the port with the port's name and the request, and the guest gets the answer", func(t *testing.T) {
		e := fresh()
		ports(e)
		r := result(t, call(t, e.instance(t), `{"op":"call","name":"ask","json":87}`))
		if r["err"] != "" || r["answer"] != "yes" {
			t.Fatalf("call = %v, want the answer yes", r)
		}
		if got := e.call.sent(); len(got) != 1 || got[0] != (sentCall{"ask", "87"}) {
			t.Fatalf("port saw %v, want ask 87", got)
		}
		if got := e.call.codes(); len(got) != 1 || got[0] != runner.CallAnswered {
			t.Fatalf("settled %v, want one answered", got)
		}
	})

	t.Run("a call to a port the manifest does not declare is denied", func(t *testing.T) {
		e := fresh()
		ports(e)
		inst := e.instance(t)
		for _, name := range []string{"power", "notify", "", "../x"} {
			r := result(t, call(t, inst, `{"op":"call","name":"`+name+`","json":1}`))
			if r["err"] != "denied" {
				t.Errorf("call %q = %v, want denied", name, r)
			}
		}
		if got := e.call.sent(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
		if got := e.call.codes(); len(got) != 4 || got[0] != runner.CallDenied {
			t.Fatalf("settled %v, want four denied", got)
		}
	})

	t.Run("a request that breaks the shape is not sent and fails the call as module.format", func(t *testing.T) {
		e := fresh()
		ports(e)
		out := call(t, e.instance(t), `{"op":"call","name":"ask","json":"87"}`)
		if out.OK || out.Kind != pdk.KindFormat || out.Error != "port ask: expected integer, got string" {
			t.Fatalf("output = %+v, want module.format with the node's words", out)
		}
		if strings.Contains(out.Error, "87") {
			t.Fatal("the error echoes the value")
		}
		if got := e.call.sent(); len(got) != 0 {
			t.Fatalf("the port saw %v, want nothing", got)
		}
		if got := e.call.codes(); len(got) != 1 || got[0] != runner.CallFormat {
			t.Fatalf("settled %v, want one format", got)
		}
	})

	t.Run("a request over 64 KiB is refused as failed and not sent", func(t *testing.T) {
		e := fresh()
		ports(e)
		big := `"` + strings.Repeat("x", runner.MaxCallPayload) + `"`
		r := result(t, call(t, e.instance(t), `{"op":"call","name":"text","json":`+big+`}`))
		if r["err"] != "failed" {
			t.Fatalf("call = %v, want failed", r)
		}
		if got := e.call.sent(); len(got) != 0 {
			t.Fatalf("the port saw %d calls, want none", len(got))
		}
	})

	t.Run("an answer over 64 KiB or outside the out shape is failed", func(t *testing.T) {
		for name, answer := range map[string]string{
			"too big":       `"` + strings.Repeat("x", runner.MaxCallPayload) + `"`,
			"wrong shape":   `12`,
			"not an answer": `{`,
		} {
			e := fresh()
			ports(e)
			e.call.answer = answer
			r := result(t, call(t, e.instance(t), `{"op":"call","name":"ask","json":1}`))
			if r["err"] != "failed" {
				t.Errorf("%s: call = %v, want failed", name, r)
			}
			if got := e.call.codes(); len(got) != 1 || got[0] != runner.CallFailed {
				t.Errorf("%s: settled %v, want one failed", name, got)
			}
		}
	})

	t.Run("a run makes at most 100 calls, and the next call starts afresh", func(t *testing.T) {
		e := fresh()
		ports(e)
		inst := e.instance(t)
		r := result(t, call(t, inst, `{"op":"call_many","name":"ask","json":1,"mib":101}`))
		if r["err"] != "failed" || r["made"] != float64(runner.MaxCallsPerRun) {
			t.Fatalf("call_many = %v, want failed after 100", r)
		}
		if got := e.call.sent(); len(got) != runner.MaxCallsPerRun {
			t.Fatalf("the port saw %d calls, want 100", len(got))
		}
		if r := result(t, call(t, inst, `{"op":"call","name":"ask","json":1}`)); r["err"] != "" {
			t.Fatalf("call in the next call = %v, want answered", r)
		}
	})

	for name, tc := range map[string]struct {
		code runner.CallCode
		want string
	}{
		"not here":    {runner.CallNotHere, "not_here"},
		"failed":      {runner.CallFailed, "failed"},
		"unavailable": {runner.CallUnavailable, "unavailable"},
		"denied":      {runner.CallDenied, "denied"},
	} {
		t.Run("the guest maps the port's code: "+name, func(t *testing.T) {
			e := fresh()
			ports(e)
			e.call.code = tc.code
			r := result(t, call(t, e.instance(t), `{"op":"call","name":"ask","json":1}`))
			if r["err"] != tc.want {
				t.Fatalf("call = %v, want err %q", r, tc.want)
			}
		})
	}
}

// runServeTests is the serving side as the runner sees it: the answer of a
// call trigger is checked against the served port's out shape.
func runServeTests(t *testing.T, fresh func() *env) {
	serve := func(e *env, op string) {
		e.manifest.Ports.Serves = map[string]runner.PortCall{
			"ask": {In: runner.Shape{Type: "integer"}, Out: runner.Shape{Type: "string"}},
		}
		e.manifest.ConfigValues = map[string]string{"op_call_ask": op}
	}
	handle := func(t *testing.T, inst Instance) pdk.Output {
		t.Helper()
		out, err := inst.Handle(context.Background(), pdk.Input{Trigger: "call:ask", Payload: json.RawMessage(`{"from":"a.c","request":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("the answer of a call trigger that fits the out shape is the result", func(t *testing.T) {
		e := fresh()
		serve(e, `{"op":"answer","json":"yes"}`)
		if out := handle(t, e.instance(t)); !out.OK || string(out.Result) != `"yes"` {
			t.Fatalf("output = %+v, want the answer", out)
		}
	})

	t.Run("an answer outside the out shape fails the run as module.format, in the node's words", func(t *testing.T) {
		e := fresh()
		serve(e, `{"op":"answer","json":12}`)
		out := handle(t, e.instance(t))
		if out.OK || out.Kind != pdk.KindFormat || out.Error != "expected string, got number" {
			t.Fatalf("output = %+v, want module.format", out)
		}
	})
}
