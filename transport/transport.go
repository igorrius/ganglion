// Package transport defines how events enter a node and replies leave it:
// Telegram and MQTT first, HTTP and Home Assistant later (docs/spec.md,
// section 5.3).
package transport

import (
	"context"

	"github.com/igorrius/ganglion/event"
)

// Sink receives events from a transport. A transport must not advance its
// own offset (for example the Telegram update offset) until Sink has
// returned nil: the event is then durably in the inbox (R-DLV-2).
type Sink func(context.Context, event.Event) error

// Transport is a channel to people or devices.
type Transport interface {
	// Name identifies the transport, e.g. "telegram".
	Name() string
	// Run receives events and hands them to sink until ctx is cancelled.
	Run(ctx context.Context, sink Sink) error
	// Send delivers one outgoing message. The outbox sender retries it, so
	// Send should honour msg.IdemKey where the channel allows it.
	Send(ctx context.Context, msg event.Outgoing) error
}
