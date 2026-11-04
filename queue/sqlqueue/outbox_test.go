package sqlqueue_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/transport"
)

func enqueue(t *testing.T, f fixture, msgs ...event.Outgoing) {
	t.Helper()
	if err := f.q.Enqueue(context.Background(), msgs...); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

func due(t *testing.T, f fixture, limit int) ([]queue.Pending, time.Time) {
	t.Helper()
	ps, next, err := f.q.Due(context.Background(), limit)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	return ps, next
}

func keys(ps []queue.Pending) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Msg.IdemKey)
	}
	return out
}

func idOf(t *testing.T, f fixture, key string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM outbox WHERE idem_key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("id of %s: %v", key, err)
	}
	return id
}

func TestDueGivesOneHeadPerConversation(t *testing.T) {
	f := setup(t)
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:a", "a2"), reply("telegram:b", "b1"))
	ps, next := due(t, f, 100)
	if got := keys(ps); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Fatalf("Due = %v, want [a1 b1]", got)
	}
	if !next.IsZero() {
		t.Fatalf("next = %v, want zero", next)
	}
	if ps[0].Msg.ID == "" || ps[0].Msg.ID != idOf(t, f, "a1") || ps[0].Msg.Text != "hi" {
		t.Fatalf("pending message = %+v", ps[0].Msg)
	}
}

func TestDueRespectsLimit(t *testing.T) {
	f := setup(t)
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:b", "b1"), reply("telegram:c", "c1"))
	if got := keys(first(due(t, f, 2))); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Fatalf("Due(2) = %v, want [a1 b1]", got)
	}
}

func first(ps []queue.Pending, _ time.Time) []queue.Pending { return ps }

func TestSameConvOnAnotherTransportIsSeparate(t *testing.T) {
	f := setup(t)
	other := reply("telegram:a", "x1")
	other.Transport = "mqtt"
	enqueue(t, f, reply("telegram:a", "a1"), other)
	if got := keys(first(due(t, f, 100))); !slices.Equal(got, []string{"a1", "x1"}) {
		t.Fatalf("Due = %v, want [a1 x1]", got)
	}
}

func TestRetryBlocksOnlyItsConversation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:a", "a2"), reply("telegram:b", "b1"))
	if err := f.q.Retry(ctx, idOf(t, f, "a1"), t0.Add(time.Minute), "timeout"); err != nil {
		t.Fatal(err)
	}
	ps, next := due(t, f, 100)
	if got := keys(ps); !slices.Equal(got, []string{"b1"}) {
		t.Fatalf("Due = %v, want [b1]", got)
	}
	if !next.Equal(t0.Add(time.Minute)) {
		t.Fatalf("next = %v, want %v", next, t0.Add(time.Minute))
	}
	f.clock.Advance(time.Minute)
	ps, _ = due(t, f, 100)
	if got := keys(ps); !slices.Equal(got, []string{"a1", "b1"}) {
		t.Fatalf("Due after the retry time = %v, want [a1 b1]", got)
	}
	if ps[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", ps[0].Attempts)
	}
	var reason string
	if err := f.db.QueryRow(`SELECT last_error FROM outbox WHERE idem_key = 'a1'`).Scan(&reason); err != nil || reason != "timeout" {
		t.Fatalf("last_error = %q, %v", reason, err)
	}
}

func TestDueNextIsEarliestRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:b", "b1"))
	if err := f.q.Retry(ctx, idOf(t, f, "a1"), t0.Add(5*time.Minute), "x"); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Retry(ctx, idOf(t, f, "b1"), t0.Add(time.Minute), "x"); err != nil {
		t.Fatal(err)
	}
	ps, next := due(t, f, 100)
	if len(ps) != 0 || !next.Equal(t0.Add(time.Minute)) {
		t.Fatalf("Due = %v, next %v; want none, %v", keys(ps), next, t0.Add(time.Minute))
	}
}

func TestSentMovesConversationOn(t *testing.T) {
	f := setup(t)
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:a", "a2"))
	if err := f.q.Sent(context.Background(), idOf(t, f, "a1"), ""); err != nil {
		t.Fatal(err)
	}
	if got := keys(first(due(t, f, 100))); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("Due = %v, want [a2]", got)
	}
	if n := count(t, f, `SELECT count(*) FROM outbox WHERE status = 'sent' AND done_ms = ?`, t0.UnixMilli()); n != 1 {
		t.Fatal("a1 not marked sent with done_ms")
	}
}

func TestFailMovesConversationOn(t *testing.T) {
	f := setup(t)
	enqueue(t, f, reply("telegram:a", "a1"), reply("telegram:a", "a2"))
	if err := f.q.Fail(context.Background(), idOf(t, f, "a1"), "blocked"); err != nil {
		t.Fatal(err)
	}
	if got := keys(first(due(t, f, 100))); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("Due = %v, want [a2]", got)
	}
	if n := count(t, f, `SELECT count(*) FROM outbox WHERE status = 'failed' AND last_error = 'blocked'`); n != 1 {
		t.Fatal("a1 not marked failed with its reason")
	}
}

func TestMarkingAFinishedMessageFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enqueue(t, f, reply("telegram:a", "a1"))
	id := idOf(t, f, "a1")
	if err := f.q.Sent(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Retry(ctx, id, t0, "x"); err == nil {
		t.Error("Retry of a sent message succeeded")
	}
	if err := f.q.Sent(ctx, "no-such-id", ""); err == nil {
		t.Error("Sent of an unknown id succeeded")
	}
}

func TestEnqueueDuplicateIsNoOp(t *testing.T) {
	f := setup(t)
	enqueue(t, f, reply("telegram:a", "a1"))
	select {
	case <-f.q.OutboxReady():
	default:
		t.Fatal("no signal after Enqueue")
	}
	enqueue(t, f, reply("telegram:a", "a1"))
	select {
	case <-f.q.OutboxReady():
		t.Fatal("signalled after a duplicate Enqueue")
	default:
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 1 {
		t.Fatalf("outbox rows = %d, want 1", n)
	}
}

func TestEnqueueRejectsIncompleteMessage(t *testing.T) {
	f := setup(t)
	err := f.q.Enqueue(context.Background(), reply("telegram:a", "ok"), event.Outgoing{IdemKey: "x", Conv: "telegram:a"})
	if err == nil {
		t.Fatal("Enqueue without a transport succeeded")
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("outbox rows = %d, want 0: Enqueue is all or nothing", n)
	}
}

func TestPartSentSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := &fakeClock{now: t0}
	f := openAt(t, path, clock)
	enqueue(t, f, reply("telegram:a", "a1"))
	if err := f.q.PartSent(context.Background(), idOf(t, f, "a1"), 2); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f = openAt(t, path, clock)
	ps, _ := due(t, f, 100)
	if len(ps) != 1 || ps[0].PartsSent != 2 {
		t.Fatalf("after reopen: %+v, want PartsSent 2", ps)
	}
}

// Review focus 3: an attachment over the limit is refused before
// anything is stored.
func TestOversizeAttachmentRefusedAtEnqueue(t *testing.T) {
	f := setup(t)
	big := reply("telegram:a", "big")
	big.Attach = &event.Attachment{Kind: event.AttachPhoto, Name: "x.png", Type: "image/png", Data: make([]byte, event.MaxAttachmentBytes+1)}
	err := f.q.Enqueue(context.Background(), reply("telegram:a", "first"), big)
	if !errors.Is(err, transport.ErrPermanent) {
		t.Fatalf("Enqueue = %v, want ErrPermanent", err)
	}
	if ps, _ := due(t, f, 10); len(ps) != 0 {
		t.Fatalf("stored %v", keys(ps))
	}
	ok := reply("telegram:a", "ok")
	ok.Attach = &event.Attachment{Kind: event.AttachPhoto, Name: "x.png", Type: "image/png", Data: make([]byte, event.MaxAttachmentBytes)}
	enqueue(t, f, ok)
	ps, _ := due(t, f, 10)
	if len(ps) != 1 || ps[0].Msg.Attach == nil || len(ps[0].Msg.Attach.Data) != event.MaxAttachmentBytes {
		t.Fatalf("the largest allowed attachment did not round-trip: %+v", ps)
	}
}

func TestSentKeepsTheRef(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enqueue(t, f, reply("telegram:a", "k"))
	if ref, err := f.q.SentRef(ctx, "k"); err != nil || ref != "" {
		t.Fatalf("before sending: %q, %v", ref, err)
	}
	if err := f.q.Sent(ctx, idOf(t, f, "k"), "77"); err != nil {
		t.Fatal(err)
	}
	if ref, err := f.q.SentRef(ctx, "k"); err != nil || ref != "77" {
		t.Fatalf("after sending: %q, %v", ref, err)
	}
	if ref, err := f.q.SentRef(ctx, "nope"); err != nil || ref != "" {
		t.Fatalf("an unknown key: %q, %v", ref, err)
	}
}
