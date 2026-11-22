package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/tetratelabs/wazero/api"
)

// CallCode is the answer of the call host function (R-BUS-6): zero or more
// is the length of the answer, which the module takes with take; less than
// zero is a refusal.
type CallCode int32

// The codes a call returns to the module. CallAnswered is not a value the
// module sees: it is the port's word for "there is an answer", and the
// module gets the answer's length instead.
const (
	CallAnswered    CallCode = 0  // the port has the answer
	CallDenied      CallCode = -1 // the manifest declares no such calls port, or no link starts at it
	CallFormat      CallCode = -2 // the request does not fit the port's in shape
	CallNotHere     CallCode = -3 // init, health, a check and a trial run call nobody
	CallFailed      CallCode = -4 // the serving run failed or timed out, the answer does not fit the port's out shape, or a limit or the node stopped the call
	CallUnavailable CallCode = -5 // nobody answers: the serving module is not running
)

// Limits of call: the size of a request and of an answer, and the calls one
// run makes.
const (
	MaxCallPayload = 64 << 10
	MaxCallsPerRun = 100
)

// CallPort sends a module's request and waits for the answer. The runner has
// already checked the port against the manifest, the request's size and
// shape and the run's limit; the run's identity and deadline travel in ctx.
// Call returns the answer with CallAnswered, or the code that refused it.
// Settled is told the code the module gets for every call, so the host
// counts each once.
type CallPort interface {
	Call(ctx context.Context, port string, request json.RawMessage) (json.RawMessage, CallCode)
	Settled(ctx context.Context, code CallCode)
}

// noCall is the port of a module loaded with none: no run calls.
type noCall struct{}

func (noCall) Call(context.Context, string, json.RawMessage) (json.RawMessage, CallCode) {
	return nil, CallNotHere
}
func (noCall) Settled(context.Context, CallCode) {}

// callCall is the call host function as the module's memory sees it: the
// port's name and the request. The answer is left for take.
func (m *Module) callCall(ctx context.Context, mod api.Module, pp, pn, vp, vn uint32) int32 {
	v, _ := mod.Memory().Read(vp, vn)
	return m.call(ctx, readString(mod, pp, pn), bytes.Clone(v))
}

// call is the call host function: the port must be a declared calls port,
// the request must fit its in shape and the run must be within its limits,
// before the port is asked; the answer must be within the size limit and
// fit the out shape. A request that breaks the shape is not sent and fails
// the run as module.format, in the node's words and never with the value
// (R-RUN-14).
func (m *Module) call(ctx context.Context, port string, request []byte) int32 {
	m.called(ctx, CallCall)
	answer, code := m.doCall(ctx, port, request)
	m.deps.Call.Settled(ctx, code)
	if code != CallAnswered {
		return int32(code)
	}
	m.pending = answer
	return int32(len(answer))
}

// doCall is call without the count: the answer, or the code that refused.
func (m *Module) doCall(ctx context.Context, port string, request []byte) ([]byte, CallCode) {
	shapes, ok := m.manifest.Ports.Calls[port]
	switch {
	case !ok:
		m.deps.Logger.Warn("call denied", "module", m.manifest.Name)
		return nil, CallDenied
	case len(request) > MaxCallPayload:
		return nil, CallFailed
	}
	if err := shapes.In.Check(request); err != nil {
		m.formatErr = fmt.Sprintf("port %s: %v", port, err)
		return nil, CallFormat
	}
	if m.calls++; m.calls > MaxCallsPerRun {
		return nil, CallFailed
	}
	answer, code := m.deps.Call.Call(ctx, port, json.RawMessage(request))
	if code != CallAnswered {
		return nil, code
	}
	if len(answer) > MaxCallPayload || shapes.Out.Check(answer) != nil {
		return nil, CallFailed
	}
	return answer, CallAnswered
}
