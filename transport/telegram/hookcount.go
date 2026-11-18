package telegram

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// hookOutcome is what became of a webhook request.
type hookOutcome string

// Outcomes of a webhook request.
const (
	hookStored     hookOutcome = "stored"      // the update is in the inbox
	hookSkipped    hookOutcome = "skipped"     // an update that becomes no event
	hookDropped    hookOutcome = "dropped"     // the sink dropped it (a stranger's)
	hookIntake     hookOutcome = "intake"      // a pasted intake line, taken before the inbox
	hookUnparsed   hookOutcome = "unparsed"    // the body is not an update
	hookSinkFailed hookOutcome = "sink_failed" // not stored: Telegram sends it again
	hookNotReady   hookOutcome = "not_ready"   // before Serve takes requests
	hookForbidden  hookOutcome = "forbidden"   // the secret is wrong or missing
	hookBadMethod  hookOutcome = "bad_method"  // not a POST
	hookTooLarge   hookOutcome = "too_large"   // the body is past the listener's cap, or was cut
)

// hookStatus is the HTTP status of each outcome. Only a failed write and a
// node that is not ready ask Telegram to send the update again.
var hookStatus = map[hookOutcome]int{
	hookStored: http.StatusOK, hookSkipped: http.StatusOK, hookDropped: http.StatusOK, hookIntake: http.StatusOK, hookUnparsed: http.StatusOK,
	hookSinkFailed: http.StatusInternalServerError, hookNotReady: http.StatusServiceUnavailable,
	hookForbidden: http.StatusForbidden, hookBadMethod: http.StatusMethodNotAllowed,
	hookTooLarge: http.StatusRequestEntityTooLarge,
}

type hookCounter struct{ c metric.Int64Counter }

func newHookCounter() hookCounter {
	c, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.telegram.webhook.requests",
		metric.WithDescription("Requests to the Telegram webhook, by outcome (stored, skipped, dropped, intake, unparsed, sink_failed, not_ready, forbidden, bad_method, too_large)."))
	return hookCounter{c: c}
}

func (h hookCounter) add(ctx context.Context, o hookOutcome) {
	h.c.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
