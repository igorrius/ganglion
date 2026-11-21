package modhost_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// busEnv is an env whose host publishes to a real embedded bus, and the
// core's consumer of it, run by the test so that it decides when messages
// are delivered. Every call of the consumer's handler is reported on
// handled with what Deliver answered.
type busEnv struct {
	*env
	bus       *natsbus.Bus
	handled   chan error
	failFirst bool
}

func newBusEnv(t *testing.T, notifyTo []string, failFirst bool) *busEnv {
	t.Helper()
	ctx := context.Background()
	b, err := natsbus.Open(ctx, natsbus.DefaultConfig(t.TempDir()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, active(t, "local/n", notifyManifest))
	if err := e.conf.SetModule(settings.Module{Name: "local/n", NotifyTo: notifyTo}); err != nil {
		t.Fatal(err)
	}
	e.pub = b
	e.host = e.newHost()
	e.start(t)
	be := &busEnv{env: e, bus: b, handled: make(chan error, 8)}
	be.failFirst = failFirst
	return be
}

// consume runs the core's consumer until the returned stop, which returns
// after the message in hand was acked. With failFirst the handler fails the
// first message after Deliver put its copies, as a crash between the put
// and the ack would.
func (be *busEnv) consume(t *testing.T) (stop func()) {
	t.Helper()
	cfg := natsbus.DefaultNoticeConsumer()
	cfg.Retry = backoff.Policy{Min: time.Millisecond, Max: 5 * time.Millisecond}
	failed := !be.failFirst
	c := be.bus.NoticeConsumer(cfg, func(ctx context.Context, id string, payload []byte) error {
		err := be.host.Deliver(ctx, id, payload)
		if err == nil && !failed {
			failed = true
			err = errors.New("the ack was lost")
		}
		be.handled <- err
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("consumer: %v", err)
		}
		if n, err := c.Pending(context.Background()); err != nil || n != 0 {
			t.Errorf("pending = %d, %v; want 0", n, err)
		}
	}
}

// handledCall waits for the consumer's next call of the handler.
func (be *busEnv) handledCall(t *testing.T) error {
	t.Helper()
	select {
	case err := <-be.handled:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer did not handle a message")
		return nil
	}
}

// streamMessages is how many messages NOTIFY holds.
func (be *busEnv) streamMessages(t *testing.T) uint64 {
	t.Helper()
	s, err := be.bus.JetStream().Stream(context.Background(), bus.StreamNotify)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

// R-BUS-7: a module's message is published once, whatever the number of
// destinations, and nothing reaches the inbox until the core's consumer
// delivers it; then the owner and the group each get one copy, under the
// keys they had before the stream.
func TestANoticeReachesEveryDestinationOnceThroughTheStream(t *testing.T) {
	be := newBusEnv(t, []string{"family-k7", "owner"}, false)
	if got := be.send(t, 1); got != "" {
		t.Fatalf("the module was told %q", got)
	}
	if got := be.streamMessages(t); got != 1 {
		t.Fatalf("NOTIFY holds %d messages, want 1 for two destinations", got)
	}
	if got := be.copies(t); len(got) != 0 {
		t.Fatalf("copies %v before the consumer ran", got)
	}
	stop := be.consume(t)
	if err := be.handledCall(t); err != nil {
		t.Fatal(err)
	}
	stop()
	const key = "notify:local/n:01TURN:1:1"
	want := []string{"telegram:-1001/45 to=family-k7 key=" + key + ":to:family-k7", "telegram:42 to= key=" + key}
	if got := be.copies(t); !slices.Equal(got, want) {
		t.Fatalf("copies %v, want %v", got, want)
	}
	if got := be.streamMessages(t); got != 1 {
		t.Fatalf("NOTIFY holds %d messages after the delivery", got)
	}
}

// A message whose ack was lost comes again; Deliver puts its copies again
// and the inbox's keys drop them, so nobody gets a message twice.
func TestARedeliveredNoticePutsNoSecondCopy(t *testing.T) {
	rec := telemetrytest.Install(t)
	be := newBusEnv(t, []string{"family-k7", "owner"}, true)
	be.send(t, 1)
	stop := be.consume(t)
	if err := be.handledCall(t); err == nil {
		t.Fatal("the first handling should have failed")
	}
	if err := be.handledCall(t); err != nil {
		t.Fatalf("the second handling: %v", err)
	}
	stop()
	if got := be.copies(t); len(got) != 2 {
		t.Fatalf("copies %v, want one for the owner and one for the group", got)
	}
	if got := rec.Value(t, "ganglion.modhost.notify.deliveries", map[string]string{"module": "local/n", "outcome": "delivered"}); got != 2 {
		t.Errorf("delivered = %v, want 2 (the message came twice)", got)
	}
}

// A module removed before its message is delivered: the message is acked,
// not sent, and counted as gone.
func TestANoticeOfARemovedModuleIsAckedAndCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	be := newBusEnv(t, nil, false)
	be.send(t, 1)
	if err := be.host.Remove(context.Background(), "n"); err != nil {
		t.Fatal(err)
	}
	stop := be.consume(t)
	if err := be.handledCall(t); err != nil {
		t.Fatalf("a message of a removed module must be acked: %v", err)
	}
	stop() // checks nothing is left pending
	if got := be.copies(t); len(got) != 0 {
		t.Fatalf("copies %v for a module that is gone", got)
	}
	if got := rec.Value(t, "ganglion.modhost.notify.deliveries", map[string]string{"module": "local/n", "outcome": "gone"}); got != 1 {
		t.Errorf("gone = %v, want 1", got)
	}
}
