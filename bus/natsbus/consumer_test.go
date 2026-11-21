package natsbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// seen is one call of a test handler.
type seen struct {
	id      string
	payload string
}

// testConsumer is a consumer with a short retry and ack wait, and a
// handler that reports each call on the returned channel and answers with
// the next of answers (nil once they are used up).
func testConsumer(b *Bus, answers ...error) (*Consumer, <-chan seen) {
	calls := make(chan seen, 16)
	cfg := DefaultNoticeConsumer()
	cfg.Retry = backoff.Policy{Min: time.Millisecond, Max: 5 * time.Millisecond}
	cfg.AckWait = 200 * time.Millisecond
	n := 0
	h := func(_ context.Context, id string, payload []byte) error {
		calls <- seen{id: id, payload: string(payload)}
		var err error
		if n < len(answers) {
			err = answers[n]
		}
		n++
		return err
	}
	return b.NoticeConsumer(cfg, h), calls
}

// run starts c and returns a function that stops it and waits for Run to
// return, which is after the message in hand was handled and acked.
func run(t *testing.T, c *Consumer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	}
}

func publish(t *testing.T, b *Bus, id, text string) {
	t.Helper()
	subject, err := bus.Notify("reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), subject, id, map[string]string{bus.HeaderFrom: "core/local/reader", bus.HeaderRun: "run-1"}, []byte(text)); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, calls <-chan seen) seen {
	t.Helper()
	select {
	case s := <-calls:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("the handler was not called")
		return seen{}
	}
}

// A published message is stored once however many times it is published,
// reaches the handler with its id and body, and is acked after the handler
// returned nil.
func TestNoticeIsStoredOnceHandledAndAcked(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publish(t, b, "notify:local/reader:r1:1", "one")
	publish(t, b, "notify:local/reader:r1:1", "one") // a publish repeated: the stream drops it
	s, err := b.JetStream().Stream(ctx, bus.StreamNotify)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := s.Info(ctx); err != nil || info.State.Msgs != 1 {
		t.Fatalf("the stream holds %d messages, %v; want 1", info.State.Msgs, err)
	}
	c, calls := testConsumer(b)
	if n, err := c.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("pending before the consumer exists = %d, %v", n, err)
	}
	stop := run(t, c)
	if got := receive(t, calls); got != (seen{id: "notify:local/reader:r1:1", payload: "one"}) {
		t.Fatalf("handled %+v", got)
	}
	stop() // Run returns after the ack
	if n, err := c.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("pending after the ack = %d, %v", n, err)
	}
	if got := rec.Value(t, "ganglion.bus.publishes", map[string]string{"outcome": "ok"}); got != 2 {
		t.Errorf("publishes ok = %v, want 2", got)
	}
	if got := rec.Value(t, "ganglion.bus.consumes", map[string]string{"consumer": "core-notify", "outcome": "ok"}); got != 1 {
		t.Errorf("consumes ok = %v, want 1", got)
	}
}

// A message the handler failed comes again, and is acked only when the
// handler took it.
func TestAFailedNoticeIsDeliveredAgain(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publish(t, b, "k1", "one")
	c, calls := testConsumer(b, errors.New("no put"))
	stop := run(t, c)
	first, second := receive(t, calls), receive(t, calls)
	if first != second {
		t.Fatalf("handled %+v then %+v, want the same message twice", first, second)
	}
	stop()
	if n, err := c.Pending(ctx); err != nil || n != 0 {
		t.Fatalf("pending = %d, %v; want 0 after the second try was acked", n, err)
	}
	for outcome, want := range map[string]float64{"ok": 1, "error": 1} {
		if got := rec.Value(t, "ganglion.bus.consumes", map[string]string{"consumer": "core-notify", "outcome": outcome}); got != want {
			t.Errorf("consumes %s = %v, want %v", outcome, got, want)
		}
	}
}

// A message in hand when the node stops is neither acked nor refused: the
// server sends it to the consumer of the next start after the ack wait.
func TestAnUnackedNoticeComesToTheNextConsumer(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	publish(t, b, "k1", "one")

	runCtx, cancel := context.WithCancel(ctx)
	got := make(chan seen, 1)
	died := b.NoticeConsumer(DefaultNoticeConsumer(), func(_ context.Context, id string, payload []byte) error {
		got <- seen{id: id, payload: string(payload)}
		cancel() // the node stops while the handler works
		return context.Canceled
	})
	died.cfg.AckWait = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- died.Run(runCtx) }()
	receive(t, got)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	c, calls := testConsumer(b)
	stop := run(t, c)
	defer stop()
	if again := receive(t, calls); again.id != "k1" {
		t.Fatalf("handled %+v", again)
	}
}
