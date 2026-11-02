package transport

import (
	"errors"
	"time"

	"github.com/igorrius/ganglion/event"
)

// ErrPermanent marks a send that will never succeed: the person blocked the
// bot, the chat is gone, the request is malformed. Transports wrap it; the
// outbox sender then gives up on the message and moves the conversation on.
var ErrPermanent = errors.New("transport: permanent failure")

// RetryAfterError is returned when the channel asks the sender to wait
// (Telegram's 429 with retry_after). The sender pauses the whole transport
// for After and does not count it as a failed attempt.
type RetryAfterError struct {
	After time.Duration
}

func (e *RetryAfterError) Error() string {
	return "transport: retry after " + e.After.String()
}

// Parter is implemented by transports with a message size limit. Parts
// splits a message into messages the channel accepts. It must be
// deterministic, so that a sender resuming after a crash sends the same
// parts; a message within the limit is returned as the only part.
type Parter interface {
	Parts(msg event.Outgoing) []event.Outgoing
}
