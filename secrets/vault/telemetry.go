package vault

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// op is what was done with the store.
type op string

// Operations.
const (
	opGet    op = "get"
	opSet    op = "set"
	opRemove op = "remove" // one secret or a whole scope
	opReset  op = "reset"
)

// outcome is how an operation ended.
type outcome string

// Outcomes.
const (
	outcomeOK     outcome = "ok"
	outcomeUnset  outcome = "unset"  // nothing stored under the name
	outcomeLocked outcome = "locked" // the key does not fit the store
	outcomeError  outcome = "error"
)

type instruments struct{ ops metric.Int64Counter }

func newInstruments() instruments {
	c, _ := otel.Meter("ganglion/secrets").Int64Counter("ganglion.secrets.ops",
		metric.WithDescription("Operations on the secret store, by operation (get, set, remove, reset) and outcome (ok; unset: nothing stored under the name; locked: the key does not fit the store; error)."))
	return instruments{ops: c}
}

// count records one operation. A scope or a name is never a label.
func (in instruments) count(ctx context.Context, o op, out outcome) {
	in.ops.Add(ctx, 1, metric.WithAttributes(attribute.String("op", string(o)), attribute.String("outcome", string(out))))
}

// done is the outcome of an operation that returned err.
func done(err error) outcome {
	if err != nil {
		return outcomeError
	}
	return outcomeOK
}
