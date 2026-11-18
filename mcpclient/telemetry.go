package mcpclient

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// callOutcome is how a call of a server's tool ended, for the metrics: a
// closed set.
type callOutcome string

// Call outcomes.
const (
	outcomeOK          callOutcome = "ok"
	outcomeToolError   callOutcome = "tool_error"  // the tool or the server answered with an error
	outcomeUnreachable callOutcome = "unreachable" // no answer, and the call is known not to have run or is a read
	outcomeUnknown     callOutcome = "unknown"     // a call that is not read-only got no answer (R-MCP-8)
	outcomeTooLarge    callOutcome = "too_large"   // the answer was over the limit and was not read
	outcomeRefused     callOutcome = "refused"     // the call was not made: the server is not connected
)

// connectOutcome is how a connect, the handshake and the first list, ended.
type connectOutcome string

// Connect outcomes.
const (
	connectOK           connectOutcome = "connected"
	connectChanged      connectOutcome = "tools_changed" // the list does not match the fingerprint (R-MCP-7)
	connectUnauthorized connectOutcome = "unauthorized"
	connectFailed       connectOutcome = "failed"
)

// instruments are the client's metrics (R-OBS-8), labelled by the servers
// the admin configured and closed sets of words; never an address or text.
type instruments struct {
	calls    metric.Int64Counter
	duration metric.Float64Histogram
	connects metric.Int64Counter
}

func newInstruments(m metric.Meter) instruments {
	var in instruments
	in.calls, _ = m.Int64Counter("ganglion.mcpclient.calls",
		metric.WithDescription("Calls of remote MCP servers' tools, by server and outcome (ok, tool_error, unreachable, unknown: not read-only and no answer, too_large, refused)."))
	in.duration, _ = m.Float64Histogram("ganglion.mcpclient.call.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("Time of calls of remote MCP servers' tools in seconds, by server and outcome."))
	in.connects, _ = m.Int64Counter("ganglion.mcpclient.connects",
		metric.WithDescription("Connects to remote MCP servers, by server and outcome (connected, tools_changed, unauthorized, failed)."))
	return in
}

// defaultInstruments are made from the global meter provider.
func defaultInstruments() instruments { return newInstruments(otel.Meter("ganglion/mcpclient")) }

// call counts one call and its time.
func (in instruments) call(ctx context.Context, server string, o callOutcome, took time.Duration) {
	attrs := metric.WithAttributes(attribute.String("server", server), attribute.String("outcome", string(o)))
	in.calls.Add(ctx, 1, attrs)
	in.duration.Record(ctx, max(took, 0).Seconds(), attrs)
}

// connect counts one connect.
func (in instruments) connect(ctx context.Context, server string, o connectOutcome) {
	in.connects.Add(ctx, 1, metric.WithAttributes(attribute.String("server", server), attribute.String("outcome", string(o))))
}
