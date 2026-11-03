package guard

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// What a check checked: a metric label.
const (
	checkFact    = "fact"
	checkPersona = "persona"
)

func newVerdicts() metric.Int64Counter {
	c, _ := otel.Meter("ganglion/guard").Int64Counter("ganglion.guard.verdicts",
		metric.WithDescription("Clean-context checks, by check (fact, persona) and verdict (ok, refused, unparseable, unavailable)."))
	return c
}

// observe counts a check's verdict; a check cut short by shutdown decided
// nothing and is not counted. The reason's text never goes into the
// metric.
func (g *Guard) observe(ctx context.Context, check string, v Verdict, err error) {
	if err != nil {
		return
	}
	verdict := "refused"
	switch {
	case v.OK:
		verdict = "ok"
	case v.Reason == ReasonUnavailable:
		verdict = "unavailable"
	case v.Reason == ReasonUnparseable:
		verdict = "unparseable"
	}
	g.verdicts.Add(ctx, 1, metric.WithAttributes(attribute.String("check", check), attribute.String("verdict", verdict)))
}
