package natsbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
// handler (R-BUS-7). It is RunConsumer with the notice consumer's settings.
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
	spec := bus.ConsumerSpec{Stream: bus.StreamNotify, Name: c.cfg.Name, Filter: bus.SubjectsNotify,
		Deliver: bus.DeliverAll, AckWait: c.cfg.AckWait, Retry: c.cfg.Retry}
	return c.b.runConsumer(ctx, spec, c.cfg.After, func(ctx context.Context, d bus.Delivery) error {
		return c.h(ctx, d.ID, d.Payload)
	})
}

// Pending is how many messages the consumer has not acked: those waiting
// and those being handled. A consumer that does not exist yet has none.
func (c *Consumer) Pending(ctx context.Context) (int, error) {
	return c.b.ConsumerPending(ctx, bus.StreamNotify, c.cfg.Name)
}

// RunConsumer reads the stream through the durable consumer spec names,
// creating it or bringing an existing one to the spec, and hands each
// message to h, one at a time, until ctx ends; it then returns nil. A
// message is acked only after h returned nil; one h failed is asked for
// again after spec.Retry's wait, with no limit on the tries. A message
// being handled when ctx ends is neither acked nor refused: the server
// sends it again after the ack wait. An error is a consumer that could not
// be made.
func (b *Bus) RunConsumer(ctx context.Context, spec bus.ConsumerSpec, h bus.DeliveryHandler) error {
	return b.runConsumer(ctx, spec, time.After, h)
}

func (b *Bus) runConsumer(ctx context.Context, spec bus.ConsumerSpec, after func(time.Duration) <-chan time.Time, h bus.DeliveryHandler) error {
	cons, err := b.createConsumer(ctx, spec)
	if err != nil {
		return err
	}
	reg, err := b.metrics.watchLag(cons, spec.Name)
	if err != nil {
		b.log.Warn("natsbus: consumer lag gauge not registered", "consumer", spec.Name, "err", err)
	} else {
		defer func() { _ = reg.Unregister() }()
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return fmt.Errorf("natsbus: consumer %s: %w", spec.Name, err)
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
			b.log.Warn("natsbus: consumer read failed", "consumer", spec.Name, "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-after(spec.Retry.Delay(failures)):
			}
		default:
			failures = 0
			b.handle(ctx, spec, m, h)
		}
	}
}

// ConsumerPending is how many messages a durable consumer has not acked:
// those waiting and those being handled. A consumer that does not exist
// has none.
func (b *Bus) ConsumerPending(ctx context.Context, stream, name string) (int, error) {
	cons, err := b.js.Consumer(ctx, stream, name)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("natsbus: consumer %s: %w", name, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("natsbus: consumer %s: %w", name, err)
	}
	return int(info.NumPending) + info.NumAckPending, nil
}

// DeleteConsumer deletes a durable consumer and what it remembers. One that
// does not exist is no error.
func (b *Bus) DeleteConsumer(ctx context.Context, stream, name string) error {
	err := b.js.DeleteConsumer(ctx, stream, name)
	if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("natsbus: delete consumer %s: %w", name, err)
	}
	return nil
}

// ConsumerNames are the names of the stream's durable consumers that start
// with prefix.
func (b *Bus) ConsumerNames(ctx context.Context, stream, prefix string) ([]string, error) {
	s, err := b.js.Stream(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	var out []string
	names := s.ConsumerNames(ctx)
	for n := range names.Name() {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	if err := names.Err(); err != nil {
		return nil, fmt.Errorf("natsbus: consumers of %s: %w", stream, err)
	}
	return out, nil
}

// createConsumer makes the durable consumer, or brings the existing one to
// these settings.
func (b *Bus) createConsumer(ctx context.Context, spec bus.ConsumerSpec) (jetstream.Consumer, error) {
	stream, err := b.js.Stream(ctx, spec.Stream)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", spec.Stream, err)
	}
	policy := jetstream.DeliverAllPolicy
	if spec.Deliver == bus.DeliverNew {
		policy = jetstream.DeliverNewPolicy
	}
	cfg := jetstream.ConsumerConfig{
		Durable:       spec.Name,
		Description:   spec.Description,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       spec.AckWait,
		MaxDeliver:    -1,
		DeliverPolicy: policy,
	}
	if len(spec.Filters) > 0 {
		cfg.FilterSubjects = spec.Filters
	} else {
		cfg.FilterSubject = spec.Filter
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("natsbus: consumer %s: %w", spec.Name, err)
	}
	return cons, nil
}

// delivery is m as the handler takes it.
func delivery(m jetstream.Msg) bus.Delivery {
	d := bus.Delivery{ID: m.Headers().Get(bus.HeaderMsgID), Payload: m.Data(), Tries: 1}
	if h := m.Headers(); len(h) > 0 {
		d.Headers = make(map[string]string, len(h))
		for k := range h {
			d.Headers[k] = h.Get(k)
		}
	}
	if md, err := m.Metadata(); err == nil {
		d.Tries, d.Seq = md.NumDelivered, md.Sequence.Stream
	}
	return d
}

// handle gives m to the handler and acks it when that succeeded, or asks
// for it again after the back-off.
func (b *Bus) handle(ctx context.Context, spec bus.ConsumerSpec, m jetstream.Msg, h bus.DeliveryHandler) {
	d := delivery(m)
	err := h(ctx, d)
	if err == nil {
		b.ack(ctx, spec.Name, m, d.ID)
		return
	}
	if ctx.Err() != nil {
		return // shutting down: the server sends it again after the ack wait
	}
	delay := spec.Retry.Delay(int(d.Tries))
	b.log.Warn("natsbus: a message was not handled; it will be delivered again", "consumer", spec.Name, "id", d.ID, "tries", d.Tries, "in", delay, "err", err)
	b.metrics.consumed(ctx, spec.Name, outcomeError)
	if nakErr := m.NakWithDelay(delay); nakErr != nil {
		b.log.Warn("natsbus: nak failed; the message comes after the ack wait", "consumer", spec.Name, "id", d.ID, "err", nakErr)
	}
}

// ack acks a message the handler took. A failed ack is logged: the message
// comes again and the handler's keys drop the copy.
func (b *Bus) ack(ctx context.Context, consumer string, m jetstream.Msg, id string) {
	// The ack waits for the server to confirm it, so a message is not
	// counted done while its ack is still on the way.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
	defer cancel()
	if err := m.DoubleAck(actx); err != nil {
		b.log.Warn("natsbus: ack failed; the message will be delivered again", "consumer", consumer, "id", id, "err", err)
		b.metrics.consumed(ctx, consumer, outcomeError)
		return
	}
	b.metrics.consumed(ctx, consumer, outcomeOK)
}
