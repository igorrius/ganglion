package sqlqueue_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/queue/sqlqueue"
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
	if err := f.q.Sent(context.Background(), idOf(t, f, "a1"), "", ""); err != nil {
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
	if err := f.q.Sent(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Retry(ctx, id, t0, "x"); err == nil {
		t.Error("Retry of a sent message succeeded")
	}
	if err := f.q.Sent(ctx, "no-such-id", "", ""); err == nil {
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
	if err := f.q.PartSent(context.Background(), idOf(t, f, "a1"), 2, "", ""); err != nil {
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
	if err := f.q.Sent(ctx, idOf(t, f, "k"), "77", ""); err != nil {
		t.Fatal(err)
	}
	if ref, err := f.q.SentRef(ctx, "k"); err != nil || ref != "77" {
		t.Fatalf("after sending: %q, %v", ref, err)
	}
	if ref, err := f.q.SentRef(ctx, "nope"); err != nil || ref != "" {
		t.Fatalf("an unknown key: %q, %v", ref, err)
	}
}

// A message resumed with every part recorded is marked sent with no ref of its
// own: it keeps the ref its last part recorded.
func TestSentKeepsAPartsRef(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enqueue(t, f, reply("telegram:a", "k"))
	id := idOf(t, f, "k")
	if err := f.q.PartSent(ctx, id, 1, "77", "77"); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Sent(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	if ref, err := f.q.SentRef(ctx, "k"); err != nil || ref != "77" {
		t.Fatalf("SentRef = %q, %v; want the part's 77", ref, err)
	}
}

func dueID(t *testing.T, f fixture) string {
	t.Helper()
	ps, _ := due(t, f, 1)
	if len(ps) != 1 {
		t.Fatalf("due messages = %d, want 1", len(ps))
	}
	return ps[0].Msg.ID
}

// R-DLV-14: a reply to any part of a split message finds the message.
func TestSentByRefFindsEveryPart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	m := reply("telegram:1", "k1:reply:0")
	m.Text = "a long answer"
	if err := f.q.Enqueue(ctx, m); err != nil {
		t.Fatal(err)
	}
	id := dueID(t, f)
	if err := f.q.PartSent(ctx, id, 1, "10", "10"); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Sent(ctx, id, "11", "11"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"10", "11"} {
		got, found, err := f.q.SentByRef(ctx, "telegram", "telegram:1", ref)
		if err != nil || !found || got.Text != "a long answer" {
			t.Fatalf("ref %s: found %v, %+v, %v", ref, found, got, err)
		}
	}
	if _, found, _ := f.q.SentByRef(ctx, "telegram", "telegram:2", "10"); found {
		t.Fatal("a ref was found in another conversation")
	}
}

// An edit with text points the message at the edit's row, so a reply reads what it
// shows now; an edit of the buttons only (no ref given) leaves it.
func TestSentByRefFollowsEditsWithText(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	send := func(key, text, ref string, o *event.Origin) {
		t.Helper()
		m := reply("telegram:1", key)
		m.Text, m.Origin = text, o
		if err := f.q.Enqueue(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := f.q.Sent(ctx, dueID(t, f), "20", ref); err != nil {
			t.Fatal(err)
		}
	}
	send("plan:1:show", "1. read", "20", &event.Origin{Kind: event.OriginTurn, Session: "s1", Tainted: true})
	send("plan:1:v2", "1. read \u2713", "20", &event.Origin{Kind: event.OriginTurn, Session: "s2"})
	send("q:1:buttons", "", "", nil)
	f.clock.Advance(sqlqueue.Retention + time.Hour)
	if _, err := f.q.Prune(ctx, sqlqueue.Retention, sqlqueue.RefRetention); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.q.SentByRef(ctx, "telegram", "telegram:1", "20")
	want := event.Outgoing{Text: "1. read \u2713", Origin: &event.Origin{Kind: event.OriginTurn, Session: "s2"}}
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("found %v, %+v, %v; want the edit's copy", found, got, err)
	}
}

// sendWithRef stores m and delivers it as one part that ref shows.
func sendWithRef(t *testing.T, f fixture, m event.Outgoing, ref string) {
	t.Helper()
	ctx := context.Background()
	if err := f.q.Enqueue(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Sent(ctx, dueID(t, f), ref, ref); err != nil {
		t.Fatal(err)
	}
}

// R-DLV-13: a reply reaches a message up to 90 days old, while the outbox keeps
// the message itself for 7: the ref keeps a copy of its text and origin.
func TestSentByRefOutlivesTheOutbox(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	m := reply("telegram:1", "k1:reply:0")
	m.Text = "Apples are red."
	m.Origin = &event.Origin{Kind: event.OriginTurn, Session: "s1", Tainted: true}
	sendWithRef(t, f, m, "10")
	sendWithRef(t, f, reply("telegram:1", "alert:1"), "11") // stored with no origin

	f.clock.Advance(sqlqueue.Retention + time.Hour)
	if _, err := f.q.Prune(ctx, sqlqueue.Retention, sqlqueue.RefRetention); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("outbox rows after the prune = %d, want 0", n)
	}
	got, found, err := f.q.SentByRef(ctx, "telegram", "telegram:1", "10")
	if err != nil || !found {
		t.Fatalf("after the outbox's prune: found %v, %v", found, err)
	}
	want := event.Outgoing{Text: "Apples are red.", Origin: m.Origin}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	got, found, err = f.q.SentByRef(ctx, "telegram", "telegram:1", "11")
	if err != nil || !found || got.Origin != nil || got.Text != "hi" {
		t.Fatalf("a message with no origin: found %v, %+v, %v", found, got, err)
	}
}

// A ref is kept RefRetention, then pruned with its copy.
func TestRefsArePrunedAfterRefRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sendWithRef(t, f, reply("telegram:1", "old"), "10")
	f.clock.Advance(30 * 24 * time.Hour)
	sendWithRef(t, f, reply("telegram:1", "new"), "11")

	f.clock.Advance(sqlqueue.RefRetention - 30*24*time.Hour + time.Hour)
	n, err := f.q.Prune(ctx, sqlqueue.Retention, sqlqueue.RefRetention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // both outbox rows and the old ref
		t.Fatalf("Prune removed %d rows, want 3", n)
	}
	if c := count(t, f, `SELECT count(*) FROM outbox_refs WHERE ref = '10'`); c != 0 {
		t.Fatalf("a ref older than RefRetention is kept")
	}
	if _, found, err := f.q.SentByRef(ctx, "telegram", "telegram:1", "10"); err != nil || found {
		t.Fatalf("a ref older than RefRetention: found %v, %v", found, err)
	}
	if _, found, err := f.q.SentByRef(ctx, "telegram", "telegram:1", "11"); err != nil || !found {
		t.Fatalf("a younger ref: found %v, %v", found, err)
	}
}

// The copy is cut at 2,000 characters, counted as runes.
func TestRefCopyIsCut(t *testing.T) {
	f := setup(t)
	m := reply("telegram:1", "long")
	m.Text = strings.Repeat("я", 5000)
	sendWithRef(t, f, m, "10")
	got, found, err := f.q.SentByRef(context.Background(), "telegram", "telegram:1", "10")
	if err != nil || !found || got.Text != strings.Repeat("я", 2000) {
		t.Fatalf("found %v, %d runes, %v; want 2000", found, utf8.RuneCountInString(got.Text), err)
	}
}
