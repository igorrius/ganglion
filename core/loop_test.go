package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
)

func memQueue(t *testing.T) *sqlqueue.Queue {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func msgEvent(key, conv, text string) event.Event {
	p, _ := json.Marshal(event.Message{Text: text, From: "telegram:user:1"})
	return event.Event{IdemKey: key, Source: "telegram", Conv: conv, Kind: "message", Payload: p}
}

func start(t *testing.T, l *Loop) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("loop did not stop")
		}
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func outbox(t *testing.T, q *sqlqueue.Queue) []queue.Pending {
	t.Helper()
	ps, _, err := q.Due(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestEcho(t *testing.T) {
	out, st, err := Echo(msgEvent("telegram:7001:5", "telegram:42", "hi"))
	if err != nil || st.Conv != "" {
		t.Fatalf("Echo: state %+v, err %v", st, err)
	}
	want := event.Outgoing{IdemKey: "telegram:7001:5:reply:0", Transport: "telegram", Conv: "telegram:42", Text: "echo: hi"}
	if len(out) != 1 || out[0] != want {
		t.Fatalf("Echo = %+v, want %+v", out, want)
	}
	if out, _, err := Echo(event.Event{Kind: "cron.tick", Source: "cron", Conv: "cron:x"}); err != nil || len(out) != 0 {
		t.Fatalf("Echo of a non-message = %+v, %v", out, err)
	}
	bad := msgEvent("k", "telegram:42", "")
	bad.Payload = json.RawMessage(`nope`)
	if out, _, err := Echo(bad); err != nil || len(out) != 0 {
		t.Fatalf("Echo of a malformed message = %+v, %v; want no reply and no error", out, err)
	}
}

func TestLoopEchoes(t *testing.T) {
	q := memQueue(t)
	start(t, NewLoop(q, q.InboxReady(), EchoHandler, slog.New(slog.DiscardHandler)))
	if err := q.Put(context.Background(), msgEvent("telegram:7001:1", "telegram:42", "hi")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the echo in the outbox", func() bool {
		ps := outbox(t, q)
		return len(ps) == 1 && ps[0].Msg.Text == "echo: hi" && ps[0].Msg.Conv == "telegram:42"
	})
}

func TestHandlerErrorLeavesLeaseToExpire(t *testing.T) {
	q := memQueue(t)
	var calls atomic.Int32
	h := func(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		if calls.Add(1) == 1 {
			return nil, queue.StateUpdate{}, errors.New("model down")
		}
		return EchoHandler(ctx, e)
	}
	const lease = 200 * time.Millisecond
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler), WithLease(lease), WithPoll(20*time.Millisecond)))
	began := time.Now()
	if err := q.Put(context.Background(), msgEvent("k1", "telegram:42", "hi")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the echo after the lease ran out", func() bool { return len(outbox(t, q)) == 1 })
	if d := time.Since(began); d < lease {
		t.Fatalf("retried after %v, before the lease ran out", d)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("handler calls = %d, want 2", n)
	}
}

// countingQueue counts Lease calls.
type countingQueue struct {
	queue.Queue
	mu     sync.Mutex
	leases int
}

func (c *countingQueue) Lease(ctx context.Context, d time.Duration) (queue.Lease, error) {
	c.mu.Lock()
	c.leases++
	c.mu.Unlock()
	return c.Queue.Lease(ctx, d)
}

func (c *countingQueue) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leases
}

func TestLoopWaitsOnReadyNotBusy(t *testing.T) {
	q := memQueue(t)
	cq := &countingQueue{Queue: q}
	start(t, NewLoop(cq, q.InboxReady(), EchoHandler, slog.New(slog.DiscardHandler), WithWorkers(1)))
	eventually(t, "the first Lease", func() bool { return cq.count() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if n := cq.count(); n != 1 {
		t.Fatalf("Lease called %d times on an idle queue, want 1", n)
	}
	if err := q.Put(context.Background(), msgEvent("k1", "telegram:42", "hi")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the echo after the ready signal", func() bool { return len(outbox(t, q)) == 1 })
	// Lease after ready: the event, then ErrEmpty.
	if n := cq.count(); n != 3 {
		t.Fatalf("Lease called %d times, want 3", n)
	}
}

func TestLoopKeepsOtherConversationsMoving(t *testing.T) {
	q := memQueue(t)
	h := func(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		if e.Conv == "telegram:1" {
			return nil, queue.StateUpdate{}, errors.New("always fails")
		}
		return EchoHandler(ctx, e)
	}
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler), WithLease(time.Hour)))
	ctx := context.Background()
	if err := q.Put(ctx, msgEvent("a", "telegram:1", "stuck")); err != nil {
		t.Fatal(err)
	}
	if err := q.Put(ctx, msgEvent("b", "telegram:2", "fine")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the other conversation's echo", func() bool { return len(outbox(t, q)) == 1 })
}

func TestWorkersRunConversationsConcurrently(t *testing.T) {
	q := memQueue(t)
	release := make(chan struct{})
	h := func(ctx context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		if e.Conv == "telegram:a" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return EchoHandler(ctx, e)
	}
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler), WithWorkers(4)))
	ctx := context.Background()
	if err := q.Put(ctx, msgEvent("a1", "telegram:a", "slow")); err != nil {
		t.Fatal(err)
	}
	if err := q.Put(ctx, msgEvent("b1", "telegram:b", "fast")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "conv b answered while conv a is busy", func() bool {
		ps := outbox(t, q)
		return len(ps) == 1 && ps[0].Msg.Conv == "telegram:b"
	})
	close(release)
	eventually(t, "conv a answered", func() bool { return len(outbox(t, q)) == 2 })
}

func TestOneConversationStaysSerial(t *testing.T) {
	q := memQueue(t)
	var alive, peak, done atomic.Int32
	h := func(_ context.Context, _ event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		n := alive.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		alive.Add(-1)
		done.Add(1)
		return nil, queue.StateUpdate{}, nil
	}
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler), WithWorkers(4)))
	for i := range 10 {
		if err := q.Put(context.Background(), msgEvent("k"+string(rune('0'+i)), "telegram:a", "x")); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "all ten handled", func() bool { return done.Load() == 10 })
	if p := peak.Load(); p != 1 {
		t.Fatalf("%d turns of one conversation ran at once", p)
	}
}

func TestTurnTimeoutCancelsHandler(t *testing.T) {
	q := memQueue(t)
	got := make(chan error, 1)
	h := func(ctx context.Context, _ event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		<-ctx.Done()
		got <- ctx.Err()
		return nil, queue.StateUpdate{}, ctx.Err()
	}
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler), WithTurnTimeout(50*time.Millisecond)))
	if err := q.Put(context.Background(), msgEvent("k1", "telegram:a", "x")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler ctx ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the turn timeout did not cancel the handler")
	}
}

func TestGiveUpAfterMaxAttempts(t *testing.T) {
	q := memQueue(t)
	var calls atomic.Int32
	h := func(context.Context, event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
		calls.Add(1)
		return nil, queue.StateUpdate{}, errors.New("always fails")
	}
	giveUp := func(e event.Event) []event.Outgoing {
		return []event.Outgoing{{IdemKey: e.IdemKey + ":give-up", Transport: "telegram", Conv: e.Conv, Text: "gave up"}}
	}
	start(t, NewLoop(q, q.InboxReady(), h, slog.New(slog.DiscardHandler),
		WithLease(100*time.Millisecond), WithPoll(20*time.Millisecond), WithMaxAttempts(2, giveUp)))
	if err := q.Put(context.Background(), msgEvent("k1", "telegram:a", "x")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the give-up reply", func() bool {
		ps := outbox(t, q)
		return len(ps) == 1 && ps[0].Msg.Text == "gave up"
	})
	if n := calls.Load(); n != 2 {
		t.Fatalf("handler called %d times, want 2", n)
	}
}
