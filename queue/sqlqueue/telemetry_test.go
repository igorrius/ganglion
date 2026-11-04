package sqlqueue_test

import (
	"context"
	"testing"

	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// The inbox and the outbox depths are gauges: a completed event and a
// sent message leave them.
func TestQueueDepthGauges(t *testing.T) {
	rec := telemetrytest.Install(t)
	f := setup(t)
	put(t, f, msg("telegram:a", "k1"), msg("telegram:b", "k2"))
	enqueue(t, f, reply("telegram:a", "r1"))
	depth := func(name string) float64 { return rec.Value(t, name, nil) }
	if in, out := depth("ganglion.inbox.pending"), depth("ganglion.outbox.pending"); in != 2 || out != 1 {
		t.Fatalf("inbox %v, outbox %v; want 2 and 1", in, out)
	}
	if err := f.q.Complete(context.Background(), lease(t, f), nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Sent(context.Background(), idOf(t, f, "r1"), ""); err != nil {
		t.Fatal(err)
	}
	if in, out := depth("ganglion.inbox.pending"), depth("ganglion.outbox.pending"); in != 1 || out != 0 {
		t.Fatalf("after one done: inbox %v, outbox %v; want 1 and 0", in, out)
	}
}
