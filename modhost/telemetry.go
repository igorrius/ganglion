package modhost

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments are the host's metrics (R-OBS-8, design section 8.6),
// labelled by modules from the installed set and closed sets of words.
type instruments struct {
	runs        metric.Int64Counter
	disables    metric.Int64Counter
	escalations metric.Int64Counter
	triggers    metric.Int64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/modhost")
	var in instruments
	in.runs, _ = m.Int64Counter("ganglion.modhost.runs",
		metric.WithDescription("Runs of the owner's modules, by module, version, trigger (cron, tool or cell) and result."))
	in.disables, _ = m.Int64Counter("ganglion.modhost.disables",
		metric.WithDescription("Modules the node turned off, by module and reason."))
	in.escalations, _ = m.Int64Counter("ganglion.modhost.escalations",
		metric.WithDescription("Escalations to the agent, by module, kind and outcome (put, held: an episode of the kind is open, failed)."))
	in.triggers, _ = m.Int64Counter("ganglion.modhost.triggers",
		metric.WithDescription("Cell triggers of the modules whose runs wrote cells, by producer module and outcome (put, failed, stopped: the chain is deeper than 4)."))
	return in
}

// run counts one run.
func (in instruments) run(ctx context.Context, l *loaded, trig trigger, r Result) {
	in.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("module", l.name), attribute.String("version", l.version),
		attribute.String("trigger", string(trig)), attribute.String("result", string(r))))
}

// disabled counts a module turned off.
func (in instruments) disabled(ctx context.Context, module string, why Reason) {
	in.disables.Add(ctx, 1, metric.WithAttributes(attribute.String("module", module), attribute.String("reason", string(why))))
}

// escalated counts an escalation.
func (in instruments) escalated(ctx context.Context, module string, kind Kind, o escalationOutcome) {
	in.escalations.Add(ctx, 1, metric.WithAttributes(attribute.String("module", module), attribute.String("kind", string(kind)),
		attribute.String("outcome", string(o))))
}

// triggered counts what became of one listener's trigger, or of a chain
// that stopped, for the producer module.
func (in instruments) triggered(ctx context.Context, module string, o triggerOutcome) {
	in.triggers.Add(ctx, 1, metric.WithAttributes(attribute.String("module", module), attribute.String("outcome", string(o))))
}
