package language

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// How a language came to be asked for: a metric label.
const (
	howChannel = "channel"
	howChosen  = "chosen"
)

// instruments are the translator's metrics.
type instruments struct {
	runs    metric.Int64Counter
	refused metric.Int64Counter
	tokens  metric.Int64Counter
	cost    metric.Float64Counter
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/language")
	var in instruments
	in.runs, _ = m.Int64Counter("ganglion.language.translations",
		metric.WithDescription("Translation runs, by language and outcome (done, partial, refused, failed, capped)."))
	in.refused, _ = m.Int64Counter("ganglion.language.texts_refused",
		metric.WithDescription("Translated texts the checks refused, by language."))
	in.tokens, _ = m.Int64Counter("ganglion.language.tokens",
		metric.WithDescription("Tokens spent translating, by language."))
	in.cost, _ = m.Float64Counter("ganglion.language.cost_usd",
		metric.WithDescription("What translating cost at catalogue prices, by language."))
	return in
}

// newAsked counts the languages people come to have, which shows which ones they want.
func newAsked() metric.Int64Counter {
	c, _ := otel.Meter("ganglion/language").Int64Counter("ganglion.language.asked",
		metric.WithDescription("A person's language became this one, by language and how (channel, chosen)."))
	return c
}
