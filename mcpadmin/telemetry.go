package mcpadmin

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// connectOutcome is how one step of a connect ended: a metric label.
type connectOutcome string

// Outcomes of a connect's steps.
const (
	outcomeSignIn    connectOutcome = "sign_in"   // the sign-in card was sent
	outcomeCard      connectOutcome = "card"      // the tools card was sent
	outcomeRefused   connectOutcome = "refused"   // a bad name, address or header; a server that wants a key
	outcomeFailed    connectOutcome = "failed"    // the server or its sign-in did not answer as it must
	outcomeConnected connectOutcome = "connected" // Connect or Ask for all pressed
	outcomeCancelled connectOutcome = "cancelled" // Cancel pressed
)

// change is a change of the section made by a press: a metric label.
type change string

// Changes of the section.
const (
	changeRule       change = "rule"
	changeDisconnect change = "disconnect"
)

// instruments are the admin's instruments (R-OBS-8).
type instruments struct {
	connects metric.Int64Counter
	changes  metric.Int64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/mcpadmin")
	var in instruments
	in.connects, _ = m.Int64Counter("ganglion.mcpadmin.connects",
		metric.WithDescription("Steps of connecting a remote MCP server, by outcome (sign_in, card, refused, failed, connected, cancelled)."))
	in.changes, _ = m.Int64Counter("ganglion.mcpadmin.changes",
		metric.WithDescription("Changes of the connected MCP servers after the connect, by kind (rule, disconnect)."))
	return in
}

func (in instruments) connect(ctx context.Context, o connectOutcome) {
	in.connects.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}

func (in instruments) change(ctx context.Context, c change) {
	in.changes.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(c))))
}
