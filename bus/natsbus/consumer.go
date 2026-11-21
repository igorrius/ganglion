package natsbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/nats-io/nats.go/jetstream"
)

// ackTimeout bounds the wait for the server to confirm an ack.
const ackTimeout = 5 * time.Second

// NoticeHandler handles one message of the NOTIFY stream: id is its
// Nats-Msg-Id and payload its body. A nil return acks the message; an error
// has the consumer ask for it again after a back-off.
type NoticeHandler func(ctx context.Context, id string, payload []byte) error

// ConsumerConfig is how the notice consumer runs. DefaultNoticeConsumer
// gives the defaults; the caller overrides fields.
type ConsumerConfig struct {
	// Name is the durable consumer's name, the same on every start so the
	// stream remembers what was acked.
	Name string
	// AckWait is how long the server waits for an ack before it sends the
	// message again, which is how a crash between handling and ack is
	// repaired.
	AckWait time.Duration
	// Retry is the wait before a message the handler failed is sent again,
	// doubling with the delivery count. Delivery never gives up.
	Retry backoff.Policy
	// After makes a channel that fires after d, for the wait after a read
	// error.
	After func(d time.Duration) <-chan time.Time
}

// DefaultNoticeConsumer is the core's consumer: name core-notify, 30 s of
// ack wait, a retry from 1 s to 5 min.
func DefaultNoticeConsumer() ConsumerConfig {
	return ConsumerConfig{
		Name:    "core-notify",
		AckWait: 30 * time.Second,
		Retry:   backoff.Policy{Min: time.Second, Max: 5 * time.Minute},
		After:   time.After,
	}
}

// Consumer is the node component that reads the NOTIFY stream through a
// durable pull consumer with explicit acks and hands every message to its
// handler (R-BUS-7).
type Consumer struct {
	b   *Bus
	cfg ConsumerConfig
	h   NoticeHandler
}

// NoticeConsumer makes the consumer of the NOTIFY stream that calls h.
func (b *Bus) NoticeConsumer(cfg ConsumerConfig, h NoticeHandler) *Consumer {
	return &Consumer{b: b, cfg: cfg, h: h}
}

// Name implements node.Component.
func (c *Consumer) Name() string { return "notify-consumer" }

// Run reads messages one at a time until ctx ends, then returns nil. A
// message is acked only after its handler returned nil; one the handler
// failed is asked for again after the retry's wait, with no limit on the
// tries. A message being handled when ctx ends is neither acked nor
// refused: the server sends it again after the ack wait.
func (c *Consumer) Run(ctx context.Context) error {
	cons, err := c.create(ctx)
	if err != nil {
		return err
	}
	reg, err := c.b.metrics.watchLag(cons, c.cfg.Name)
	if err != nil {
		c.b.log.Warn("natsbus: consumer lag gauge not registered", "consumer", c.cfg.Name, "err", err)
	} else {
		defer func() { _ = reg.Unregister() }()
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return fmt.Errorf("natsbus: consumer %s: %w", c.cfg.Name, err)
	}
	stop := context.AfterFunc(ctx, it.Stop)
	defer stop()
	defer it.Stop()
	failures := 0
	for {
		m, err := it.Next()
		switch {
		case ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed):
			return nil
		case err != nil:
			failures++
			c.b.log.Warn("natsbus: consumer read failed", "consumer", c.cfg.Name, "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-c.cfg.After(c.cfg.Retry.Delay(failures)):
			}
		default:
			failures = 0
			c.handle(ctx, m)
		}
	}
}

// Pending is how many messages the consumer has not acked: those waiting
// and those being handled. A consumer that does not exist yet has none.
func (c *Consumer) Pending(ctx context.Context) (int, error) {
	cons, err := c.b.js.Consumer(ctx, bus.StreamNotify, c.cfg.Name)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("natsbus: consumer %s: %w", c.cfg.Name, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("natsbus: consumer %s: %w", c.cfg.Name, err)
	}
	return int(info.NumPending) + info.NumAckPending, nil
}

// create makes the durable consumer on the NOTIFY stream, or brings the
// existing one to these settings.
func (c *Consumer) create(ctx context.Context) (jetstream.Consumer, error) {
	stream, err := c.b.js.Stream(ctx, bus.StreamNotify)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", bus.StreamNotify, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       c.cfg.Name,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       c.cfg.AckWait,
		MaxDeliver:    -1,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: bus.SubjectsNotify,
	})
	if err != nil {
		return nil, fmt.Errorf("natsbus: consumer %s: %w", c.cfg.Name, err)
	}
	return cons, nil
}

// handle gives m to the handler and acks it when that succeeded, or asks
// for it again after the back-off.
func (c *Consumer) handle(ctx context.Context, m jetstream.Msg) {
	id := m.Headers().Get(bus.HeaderMsgID)
	err := c.h(ctx, id, m.Data())
	if err == nil {
		c.ack(ctx, m, id)
		return
	}
	if ctx.Err() != nil {
		return // shutting down: the server sends it again after the ack wait
	}
	var tries uint64 = 1
	if md, mdErr := m.Metadata(); mdErr == nil {
		tries = md.NumDelivered
	}
	delay := c.cfg.Retry.Delay(int(tries))
	c.b.log.Warn("natsbus: a message was not handled; it will be delivered again", "consumer", c.cfg.Name, "id", id, "tries", tries, "in", delay, "err", err)
	c.b.metrics.consumed(ctx, c.cfg.Name, outcomeError)
	if nakErr := m.NakWithDelay(delay); nakErr != nil {
		c.b.log.Warn("natsbus: nak failed; the message comes after the ack wait", "consumer", c.cfg.Name, "id", id, "err", nakErr)
	}
}

// ack acks a message the handler took. A failed ack is logged: the message
// comes again and the handler's keys drop the copy.
func (c *Consumer) ack(ctx context.Context, m jetstream.Msg, id string) {
	// The ack waits for the server to confirm it, so a message is not
	// counted done while its ack is still on the way.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
	defer cancel()
	if err := m.DoubleAck(actx); err != nil {
		c.b.log.Warn("natsbus: ack failed; the message will be delivered again", "consumer", c.cfg.Name, "id", id, "err", err)
		c.b.metrics.consumed(ctx, c.cfg.Name, outcomeError)
		return
	}
	c.b.metrics.consumed(ctx, c.cfg.Name, outcomeOK)
}
