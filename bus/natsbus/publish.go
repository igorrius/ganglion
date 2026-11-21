package natsbus

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// publishTimeout bounds one publish: JetStream answers when the message is
// stored, and a server that does not answer is an error, not a wait.
const publishTimeout = 5 * time.Second

// Publish stores one message in the stream that covers subject and returns
// when JetStream has acknowledged it. id is the idempotency key
// (Nats-Msg-Id): a message whose id the stream already holds within its
// duplicate window is dropped and counts as published. headers are added
// as they are, the names being those of package bus. It implements the
// publisher interfaces of the packages that publish.
func (b *Bus) Publish(ctx context.Context, subject, id string, headers map[string]string, payload []byte) error {
	if b.nc == nil {
		b.metrics.published(ctx, outcomeError)
		return bus.ErrNotConnected
	}
	msg := nats.NewMsg(subject)
	msg.Data = payload
	for k, v := range headers {
		msg.Header.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if _, err := b.js.PublishMsg(ctx, msg, jetstream.WithMsgID(id)); err != nil {
		b.metrics.published(ctx, outcomeError)
		return fmt.Errorf("natsbus: publish %s: %w", subject, err)
	}
	b.metrics.published(ctx, outcomeOK)
	return nil
}
