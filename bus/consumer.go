package bus

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/internal/backoff"
)

// DeliverPolicy says where a durable consumer starts when it is created.
type DeliverPolicy string

// The policies.
const (
	// DeliverAll starts at the oldest message the stream holds.
	DeliverAll DeliverPolicy = "all"
	// DeliverNew starts at the messages published after the consumer is
	// created: it does not replay the stream's history.
	DeliverNew DeliverPolicy = "new"
)

// ConsumerSpec is a durable consumer with explicit acks (R-BUS-6, R-BUS-7).
// The stream remembers what the consumer acked under Name, so the same spec
// on every start continues where it stopped.
type ConsumerSpec struct {
	Stream string
	// Name is the durable consumer's name: letters, digits, _ and -.
	Name string
	// Description is kept with the consumer on the server, for an operator.
	Description string
	// Filter is the subject the consumer reads.
	Filter  string
	Deliver DeliverPolicy
	// AckWait is how long the server waits for an ack before it sends the
	// message again, which is how a crash between handling and ack is
	// repaired.
	AckWait time.Duration
	// Retry is the wait before a message the handler failed is sent again,
	// doubling with the delivery count. Delivery never gives up.
	Retry backoff.Policy
}

// Delivery is one message handed to a consumer's handler.
type Delivery struct {
	// ID is the message's Nats-Msg-Id.
	ID string
	// Headers are the message's headers, the first value of each.
	Headers map[string]string
	Payload []byte
	// Seq is the message's sequence number in its stream.
	Seq uint64
	// Tries is how many times the message was delivered, this one included.
	Tries uint64
}

// DeliveryHandler handles one message: a nil return acks it; an error has
// the consumer ask for it again after the back-off.
type DeliveryHandler func(ctx context.Context, d Delivery) error
