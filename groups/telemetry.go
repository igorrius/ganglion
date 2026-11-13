package groups

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcome is what became of a group that is not connected.
type outcome string

// Outcomes.
const (
	outcomeAsked   outcome = "asked"    // a request went to an admin
	outcomeKnown   outcome = "known"    // a request waits already, or was refused
	outcomeCapped  outcome = "capped"   // MaxPending requests wait
	outcomeNoAdmin outcome = "no_admin" // no admin with a private chat
)

type instruments struct{ requests metric.Int64Counter }

func newInstruments() instruments {
	c, _ := otel.Meter("ganglion/groups").Int64Counter("ganglion.groups.requests",
		metric.WithDescription("Groups that are not connected and reached the bot, by outcome (asked: a request went to an admin; known: one waits or was refused; capped: too many wait; no_admin: nobody to ask)."))
	return instruments{requests: c}
}

func (in instruments) count(ctx context.Context, o outcome) {
	in.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
