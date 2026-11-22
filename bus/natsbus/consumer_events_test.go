package natsbus

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/internal/backoff"
)

func linkSpec(name, filter string) bus.ConsumerSpec {
	return bus.ConsumerSpec{Stream: bus.StreamEvents, Name: name, Description: "a → b", Filter: filter, Deliver: bus.DeliverNew,
		AckWait: 200 * time.Millisecond, Retry: backoff.Policy{Min: time.Millisecond, Max: 5 * time.Millisecond}}
}

func publishEvent(t *testing.T, b *Bus, subject, id, body string) {
	t.Helper()
	headers := map[string]string{bus.HeaderFrom: "core/local/a", bus.HeaderRun: "run-1", bus.HeaderDepth: "2"}
	if err := b.Publish(context.Background(), subject, id, headers, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

// runConsumer starts a consumer and returns a stop that waits for it.
func runConsumer(t *testing.T, b *Bus, spec bus.ConsumerSpec, h bus.DeliveryHandler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.RunConsumer(ctx, spec, h) }()
	// A consumer that starts at the new messages reads none published
	// before it exists: wait until the server has it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := b.JetStream().Consumer(context.Background(), spec.Stream, spec.Name); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the consumer was not made")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("RunConsumer: %v", err)
		}
	}
}

func receiveDelivery(t *testing.T, ch <-chan bus.Delivery) bus.Delivery {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the handler was not called")
		return bus.Delivery{}
	}
}

// A consumer with the deliver-new policy does not replay what the stream
// held before it was made; it reads only its filter's subject, and the
// handler gets the message's id, headers, body, stream sequence and count.
func TestAnEventConsumerStartsAtNowAndReadsItsFilter(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publishEvent(t, b, "g.ev.core.a.reading", "old", "1")
	got := make(chan bus.Delivery, 8)
	spec := linkSpec("link_one", "g.ev.core.a.reading")
	stop := runConsumer(t, b, spec, func(_ context.Context, d bus.Delivery) error { got <- d; return nil })
	defer stop()
	publishEvent(t, b, "g.ev.core.a.other", "other", "2") // another port: not read
	publishEvent(t, b, "g.ev.core.a.reading", "new", "3")
	d := receiveDelivery(t, got)
	if d.ID != "new" || string(d.Payload) != "3" || d.Headers[bus.HeaderDepth] != "2" || d.Headers[bus.HeaderRun] != "run-1" ||
		d.Headers[bus.HeaderFrom] != "core/local/a" || d.Tries != 1 || d.Seq != 3 {
		t.Fatalf("delivery = %+v, want the new reading with its headers, sequence 3, first try", d)
	}
	select {
	case extra := <-got:
		t.Fatalf("the consumer also saw %+v", extra)
	default:
	}
}

// Two consumers on the same subject each get every message: one link per
// receiver.
func TestTwoEventConsumersEachReadEveryMessage(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	one, two := make(chan bus.Delivery, 4), make(chan bus.Delivery, 4)
	stop1 := runConsumer(t, b, linkSpec("link_one", "g.ev.core.a.reading"), func(_ context.Context, d bus.Delivery) error { one <- d; return nil })
	defer stop1()
	stop2 := runConsumer(t, b, linkSpec("link_two", "g.ev.core.a.reading"), func(_ context.Context, d bus.Delivery) error { two <- d; return nil })
	defer stop2()
	publishEvent(t, b, "g.ev.core.a.reading", "k1", "1")
	if a, c := receiveDelivery(t, one), receiveDelivery(t, two); a.ID != "k1" || c.ID != "k1" {
		t.Fatalf("got %+v and %+v", a, c)
	}
}

// A failed message comes again with the delivery count raised.
func TestAFailedEventIsDeliveredAgainWithItsCount(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan bus.Delivery, 8)
	n := 0
	stop := runConsumer(t, b, linkSpec("link_one", "g.ev.core.a.reading"), func(_ context.Context, d bus.Delivery) error {
		got <- d
		n++
		if n == 1 {
			return errors.New("not yet")
		}
		return nil
	})
	defer stop()
	publishEvent(t, b, "g.ev.core.a.reading", "k1", "1")
	first, second := receiveDelivery(t, got), receiveDelivery(t, got)
	if first.Tries != 1 || second.Tries != 2 || first.Seq != second.Seq {
		t.Fatalf("deliveries %+v then %+v, want the same message on tries 1 and 2", first, second)
	}
}

// A consumer that is not running keeps what it has not read; the messages
// wait for it. A deleted one forgets them, and the lister and the deleter
// agree.
func TestAnEventConsumerKeepsItsMessagesUntilItIsDeleted(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	spec := linkSpec("link_one", "g.ev.core.a.reading")
	got := make(chan bus.Delivery, 4)
	h := func(_ context.Context, d bus.Delivery) error { got <- d; return nil }
	runConsumer(t, b, spec, h)() // made, then stopped
	publishEvent(t, b, "g.ev.core.a.reading", "k1", "1")
	if n, err := b.ConsumerPending(ctx, bus.StreamEvents, "link_one"); err != nil || n != 1 {
		t.Fatalf("pending = %d, %v; want the message to wait", n, err)
	}
	stop := runConsumer(t, b, spec, h)
	if d := receiveDelivery(t, got); d.ID != "k1" {
		t.Fatalf("delivery %+v", d)
	}
	stop()

	names, err := b.ConsumerNames(ctx, bus.StreamEvents, "link_")
	if err != nil || !slices.Equal(names, []string{"link_one"}) {
		t.Fatalf("names = %v, %v", names, err)
	}
	if err := b.DeleteConsumer(ctx, bus.StreamEvents, "link_one"); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteConsumer(ctx, bus.StreamEvents, "link_one"); err != nil {
		t.Fatalf("deleting again: %v, want none", err)
	}
	if names, err := b.ConsumerNames(ctx, bus.StreamEvents, "link_"); err != nil || len(names) != 0 {
		t.Fatalf("names after the delete = %v, %v", names, err)
	}
}
