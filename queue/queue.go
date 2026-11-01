// Package queue defines the inbox/outbox that gives ganglion at-least-once
// delivery with idempotency. Inside a node it is backed by SQLite; between
// nodes (P2) by NATS JetStream (docs/spec.md, section 7.1).
package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/igorrius/ganglion/event"
)

// Lease is an event taken for processing. If the holder does not complete
// it before Until, the event becomes available again (R-DLV-3).
type Lease struct {
	Event event.Event
	Token string    // proves ownership when completing
	Until time.Time // lease expiry
}

// StateUpdate is the conversation state written together with a turn's
// replies.
type StateUpdate struct {
	Conv string
	Data json.RawMessage
}

// Queue is the inbox/outbox of a node.
type Queue interface {
	// Put stores an incoming event. An event whose IdemKey is already stored
	// is a no-op, not an error (R-DLV-1).
	Put(ctx context.Context, e event.Event) error
	// Lease takes the next unprocessed event for d.
	Lease(ctx context.Context, d time.Duration) (Lease, error)
	// Complete writes the replies, the new conversation state and the
	// "processed" mark in one transaction (R-DLV-4).
	Complete(ctx context.Context, l Lease, out []event.Outgoing, state StateUpdate) error
}
