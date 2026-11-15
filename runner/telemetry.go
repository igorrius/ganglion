package runner

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// callResult is how a module call ended: a closed set of metric labels.
type callResult string

// Call results. Out of memory is a trap: a module that cannot grow its
// memory aborts, and the runner cannot tell that from another abort.
const (
	resultOK        callResult = "ok"         // the module answered OK
	resultFailed    callResult = "failed"     // the module reported a failure
	resultTimeout   callResult = "timeout"    // stopped at the manifest's timeout
	resultCanceled  callResult = "canceled"   // stopped by the caller's context
	resultTrap      callResult = "trap"       // the module trapped or aborted
	resultBadOutput callResult = "bad_output" // the answer could not be read
	resultError     callResult = "error"      // the runner could not make the call
)

// instruments are a module's metrics (R-OBS-8), labelled by the module's
// name from its manifest: the installed set.
type instruments struct {
	calls metric.Int64Counter
	dur   metric.Float64Histogram
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/runner")
	var in instruments
	in.calls, _ = m.Int64Counter("ganglion.module.calls", metric.WithDescription("WASM module calls, by module and result."))
	in.dur, _ = m.Float64Histogram("ganglion.module.call.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("WASM module call duration, by module."))
	return in
}

// observe counts and times one call.
func (m *Module) observe(ctx context.Context, result callResult, took time.Duration) {
	name := attribute.String("module", m.manifest.Name)
	m.in.calls.Add(ctx, 1, metric.WithAttributes(name, attribute.String("result", string(result))))
	m.in.dur.Record(ctx, took.Seconds(), metric.WithAttributes(name))
}
