package publichttp

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// routeOther labels a request to a path nobody registered: the path itself
// is the client's text and never a label.
const routeOther = "other"

type instruments struct{ requests metric.Int64Counter }

func newInstruments() instruments {
	c, _ := otel.Meter("ganglion/publichttp").Int64Counter("ganglion.http.requests",
		metric.WithDescription("Requests to the public listener, by route (a registered path, or \"other\") and HTTP status."))
	return instruments{requests: c}
}

func (in instruments) count(ctx context.Context, route string, code int) {
	in.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route), attribute.String("code", strconv.Itoa(code))))
}
