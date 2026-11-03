package web

import (
	"context"
	"errors"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// fetchResults name a fetch's errors for the metrics, in the order they
// are checked: a closed set, never the address or the server's text.
var fetchResults = []struct {
	err    error
	result string
}{
	{ErrNotAllowed, "not_allowed"},
	{ErrPrivateAddress, "private_address"},
	{ErrOutsideGrant, "outside_grant"},
	{ErrTooManyRedirects, "too_many_redirects"},
	{ErrTimeout, "timeout"},
	{ErrNotFound, "not_found"},
	{ErrRequest, "request"},
	{ErrStatus, "status"},
	{ErrTooLarge, "too_large"},
	{ErrContentType, "content_type"},
	{ErrConvert, "convert"},
}

// instruments are the fetcher's metrics (R-OBS-8).
type instruments struct {
	fetches metric.Int64Counter
	bytes   metric.Int64Histogram
}

func newInstruments() instruments {
	m := otel.Meter("ganglion/web")
	var in instruments
	in.fetches, _ = m.Int64Counter("ganglion.web.fetches",
		metric.WithDescription("web.fetch reads, by result: ok or the kind of refusal or failure."))
	in.bytes, _ = m.Int64Histogram("ganglion.web.fetch.bytes", metric.WithUnit("By"), buckets.Bytes,
		metric.WithDescription("Size of a response body read within the limit."))
	return in
}

// observe counts a fetch by the kind of its error.
func (in instruments) observe(ctx context.Context, err error) {
	result := "ok"
	if err != nil {
		result = "error"
		for _, r := range fetchResults {
			if errors.Is(err, r.err) {
				result = r.result
				break
			}
		}
	}
	in.fetches.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
