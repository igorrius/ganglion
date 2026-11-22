package runner

import (
	"context"
	"encoding/json"
	"fmt"
)

// EmitCode is the answer of the emit host function (R-BUS-6).
type EmitCode int32

// The codes an emit call returns to the module.
const (
	EmitSent    EmitCode = 0  // the event was handed to the bus
	EmitDenied  EmitCode = -1 // the manifest declares no such emits port
	EmitFormat  EmitCode = -2 // the payload does not fit the port's shape
	EmitNotHere EmitCode = -3 // init, health and a check send nothing
	EmitFailed  EmitCode = -4 // over a limit, or the node could not publish it; nothing was sent
)

// Limits of emit: the size of one payload and the events one run sends.
const (
	MaxEmitPayload = 64 << 10
	MaxEmitsPerRun = 100
)

// EmitPort publishes a module's event. The runner has already checked the
// port against the manifest, the payload's size and shape, and the run's
// limit; the run's identity travels in ctx (the host sets it). Refused is
// told every call the runner answered itself, with the code it gave, so
// the host counts them too.
type EmitPort interface {
	Emit(ctx context.Context, port string, payload json.RawMessage) EmitCode
	Refused(ctx context.Context, code EmitCode)
}

// noEmit is the port of a module loaded with none: no run sends.
type noEmit struct{}

func (noEmit) Emit(context.Context, string, json.RawMessage) EmitCode { return EmitNotHere }
func (noEmit) Refused(context.Context, EmitCode)                      {}

// emit is the emit host function: the port must be a declared emits port
// (the implicit notify output is not sent by hand), the payload must fit
// its shape, and the run must be within its limits, before the port is
// asked. A payload that breaks the shape is not sent and fails the call as
// module.format, in the node's words and never with the value (R-RUN-14).
func (m *Module) emit(ctx context.Context, port string, payload []byte) int32 {
	m.called(ctx, CallEmit)
	code := m.checkEmit(port, payload)
	if code == EmitSent {
		m.emitted++
		if m.emitted > MaxEmitsPerRun {
			code = EmitFailed
		}
	}
	if code != EmitSent {
		m.deps.Emit.Refused(ctx, code)
		return int32(code)
	}
	return int32(m.deps.Emit.Emit(ctx, port, json.RawMessage(payload)))
}

// checkEmit is the runner's own part of an emit call.
func (m *Module) checkEmit(port string, payload []byte) EmitCode {
	shape, ok := m.manifest.Ports.Emits[port]
	if !ok {
		m.deps.Logger.Warn("emit denied", "module", m.manifest.Name)
		return EmitDenied
	}
	if len(payload) > MaxEmitPayload {
		return EmitFailed
	}
	if err := shape.Check(payload); err != nil {
		m.formatErr = fmt.Sprintf("port %s: %v", port, err)
		return EmitFormat
	}
	return EmitSent
}
