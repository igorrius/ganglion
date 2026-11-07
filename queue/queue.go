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
	Event    event.Event
	Token    string    // proves ownership when completing
	Until    time.Time // lease expiry
	Attempts int       // times the event was leased, this lease included; a run cut short by a restart does not count
	// Interrupted is how many runs of the event a process death or a restart
	// cut short (ADR-0046).
	Interrupted int
}

// StateUpdate is the conversation state written together with a turn's
// replies.
type StateUpdate struct {
	Conv string
	Data json.RawMessage
	// Taken are the ids of events behind the leased one that the turn took
	// (R-LIVE-1). Complete marks them done with it. An id that is not a
	// pending event of the leased event's conversation changes nothing.
	Taken []string
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
	// Behind returns the pending events of e's conversation stored after e,
	// oldest first, at most limit of them (R-LIVE-1). An unknown e has
	// nothing behind it. It changes nothing: an event is taken only when
	// Complete marks it.
	Behind(ctx context.Context, e event.Event, limit int) ([]event.Event, error)
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
	// PartSent records that the first parts parts of a message are delivered,
	// so a resumed sender does not send them again; ref, when not "", is what
	// the transport calls the last of them, kept as the message's until another
	// is given; shown, when not "", now shows this message's text (R-DLV-14).
	PartSent(ctx context.Context, id string, parts int, ref, shown string) error
	// SentByRef is what a reply needs of the delivered message a part of which
	// ref on transport in conv shows: its Text, cut at 2,000 characters, and its
	// Origin, kept up to 90 days though the message itself may be pruned
	// sooner (R-DLV-13). Found is false when no ref matches (R-DLV-14).
	SentByRef(ctx context.Context, transport, conv, ref string) (event.Outgoing, bool, error)
	// Sent marks the message delivered, its last part as PartSent would record
	// it: ref is what the transport calls that part, "" keeping the ref an
	// earlier part recorded; shown, when not "", now shows this message's text.
	Sent(ctx context.Context, id, ref, shown string) error
	// SentRef is the ref of the delivered message with idemKey, "" when it
	// is unknown, not delivered yet or has no ref.
	SentRef(ctx context.Context, idemKey string) (string, error)
	// Retry counts a failed attempt and holds the message until notBefore.
	Retry(ctx context.Context, id string, notBefore time.Time, reason string) error
	// Fail gives up on the message for good; its conversation moves on.
	Fail(ctx context.Context, id string, reason string) error
}
