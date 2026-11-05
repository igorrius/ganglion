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

// A taken event is counted, and so is one that could not be taken: it says
// the agent and the queue disagree about what waits behind a turn.
func TestTakenEventsAreCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"), msg("telegram:b", "b1"))
	l := lease(t, f)
	taken := []string{idOfInbox(t, f, "a2"), idOfInbox(t, f, "b1")}
	if err := f.q.Complete(context.Background(), l, nil, queue.StateUpdate{Taken: taken}); err != nil {
		t.Fatal(err)
	}
	for outcome, want := range map[string]float64{"taken": 1, "ignored": 1} {
		if got := rec.Value(t, "ganglion.inbox.taken", map[string]string{"outcome": outcome}); got != want {
			t.Fatalf("%s = %v, want %v", outcome, got, want)
		}
	}
}

func idOfInbox(t *testing.T, f fixture, key string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRowContext(context.Background(), `SELECT id FROM inbox WHERE idem_key = ?`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
