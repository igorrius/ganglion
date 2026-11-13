package telegram

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// groupOutcome is what became of a message in a group.
type groupOutcome string

// Group outcomes.
const (
	groupAddressed   groupOutcome = "addressed"   // handed to the sink
	groupUnaddressed groupOutcome = "unaddressed" // dropped before the sink (R-DLV-16)
)

// groupCounter counts messages in groups by outcome.
type groupCounter struct{ c metric.Int64Counter }

func newGroupCounter() groupCounter {
	c, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.telegram.group_messages",
		metric.WithDescription("Messages in groups, by outcome (addressed: handed on; unaddressed: dropped before the inbox)."))
	return groupCounter{c: c}
}

func (g groupCounter) add(ctx context.Context, o groupOutcome) {
	g.c.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
