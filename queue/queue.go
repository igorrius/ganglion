// Package queue defines the inbox/outbox that gives ganglion at-least-once
// delivery with idempotency. Inside a node it is backed by SQLite; between
// nodes (P2) by NATS JetStream (docs/spec.md, section 7.1).
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/igorrius/ganglion/event"
)

// ErrEmpty is returned by Lease when no event is ready.
var ErrEmpty = errors.New("queue: nothing to lease")

// ErrLeaseLost is returned by Complete when the lease is no longer held:
// it expired and someone else took the event, or the event is done.
var ErrLeaseLost = errors.New("queue: lease lost")

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
	// Lease takes the next unprocessed event for d. It returns ErrEmpty
	// when no event is ready.
	Lease(ctx context.Context, d time.Duration) (Lease, error)
	// Complete writes the replies, the new conversation state and the
	// "processed" mark in one transaction (R-DLV-4). It returns
	// ErrLeaseLost if l is no longer held.
	Complete(ctx context.Context, l Lease, out []event.Outgoing, state StateUpdate) error
}

// Pending is an outgoing message waiting in the outbox.
type Pending struct {
	Msg       event.Outgoing
	Attempts  int // failed attempts so far
	PartsSent int // parts already delivered, for a message the transport splits
}

// Outbox is the sending side of the queue, used by the outbox sender. Each
// conversation is delivered in order: only its oldest unsent message is
// ever due.
type Outbox interface {
	// Enqueue stores messages outside a turn (alerts, notifiers). A message
	// whose IdemKey is already stored is a no-op.
	Enqueue(ctx context.Context, msgs ...event.Outgoing) error
	// Due returns the oldest unsent message of each conversation whose
	// retry time has come, oldest first, at most limit of them, and the
	// earliest retry time among conversations not due yet (zero if none).
	Due(ctx context.Context, limit int) (due []Pending, next time.Time, err error)
	// PartSent records that the first parts parts of a split message are
	// delivered, so a resumed sender does not send them again.
	PartSent(ctx context.Context, id string, parts int) error
	// Sent marks the message delivered.
	Sent(ctx context.Context, id string) error
	// Retry counts a failed attempt and holds the message until notBefore.
	Retry(ctx context.Context, id string, notBefore time.Time, reason string) error
	// Fail gives up on the message for good; its conversation moves on.
	Fail(ctx context.Context, id string, reason string) error
}
