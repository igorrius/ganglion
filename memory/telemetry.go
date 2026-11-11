package memory

import (
	"context"
	"errors"

	"github.com/igorrius/ganglion/gitrepo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments are the memory's metrics (conventions §7). No label carries
// a topic, a fact or a person.
type instruments struct {
	changes metric.Int64Counter
	undos   metric.Int64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/memory")
	var in instruments
	in.changes, _ = m.Int64Counter("ganglion.memory.changes",
		metric.WithDescription("Memory changes, by kind (add, replace, remove, drop) and result (stored, held)."))
	in.undos, _ = m.Int64Counter("ganglion.memory.undos",
		metric.WithDescription("Undos of memory writes, by result (ok, conflict, failed)."))
	return in
}

// observeChanges counts what an Apply did; a rerun that committed nothing
// new counts nothing.
func (m *Memory) observeChanges(ctx context.Context, cs []Change, effective bool) {
	if !effective {
		return
	}
	for _, c := range cs {
		result := "stored"
		if c.Held {
			result = "held"
		}
		m.in.changes.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(c.Kind)), attribute.String("result", result)))
	}
}

// observeUndo counts an undo by its result.
func (m *Memory) observeUndo(ctx context.Context, err error) {
	result := "ok"
	switch {
	case errors.Is(err, gitrepo.ErrConflict):
		result = "conflict"
	case err != nil:
		result = "failed"
	}
	m.in.undos.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
