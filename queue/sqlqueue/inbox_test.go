package sqlqueue_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	db    *sql.DB
	q     *sqlqueue.Queue
	clock *fakeClock
}

func openAt(t *testing.T, path string, clock *fakeClock) fixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := sqlqueue.New(ctx, db, sqlqueue.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, q: q, clock: clock}
}

func setup(t *testing.T) fixture {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "queue.db"), &fakeClock{now: t0})
}

func msg(conv, key string) event.Event {
	return event.Event{
		IdemKey: key,
		Source:  "telegram",
		Conv:    conv,
		Kind:    "message",
		Payload: json.RawMessage(`{"text":"hi","from":"telegram:user:1"}`),
		At:      t0,
	}
}

func reply(conv, key string) event.Outgoing {
	return event.Outgoing{IdemKey: key, Transport: "telegram", Conv: conv, Text: "hi"}
}

func put(t *testing.T, f fixture, es ...event.Event) {
	t.Helper()
	for _, e := range es {
		if err := f.q.Put(context.Background(), e); err != nil {
			t.Fatalf("Put %s: %v", e.IdemKey, err)
		}
	}
}

func lease(t *testing.T, f fixture) queue.Lease {
	t.Helper()
	l, err := f.q.Lease(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	return l
}

func count(t *testing.T, f fixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPutDuplicateIsNoOp(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k1"))
	if n := count(t, f, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("inbox rows = %d, want 1", n)
	}
}

func TestLeaseEmpty(t *testing.T) {
	f := setup(t)
	if _, err := f.q.Lease(context.Background(), time.Minute); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("Lease on empty queue: %v, want ErrEmpty", err)
	}
}

func TestLeaseOrderPerConversation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:a", "a1"), msg("telegram:b", "b1"), msg("telegram:a", "a2"))

	first := lease(t, f)
	if first.Event.IdemKey != "a1" {
		t.Fatalf("first lease = %s, want a1", first.Event.IdemKey)
	}
	if second := lease(t, f); second.Event.IdemKey != "b1" {
		t.Fatalf("second lease = %s, want b1", second.Event.IdemKey)
	}
	if _, err := f.q.Lease(ctx, time.Minute); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("a2 leasable while a1 is in flight: %v", err)
	}
	if err := f.q.Complete(ctx, first, nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	if third := lease(t, f); third.Event.IdemKey != "a2" {
		t.Fatalf("third lease = %s, want a2", third.Event.IdemKey)
	}
}

func TestLeaseRoundTripsEvent(t *testing.T) {
	f := setup(t)
	e := msg("telegram:1", "k1")
	e.TTL = time.Hour
	put(t, f, e)
	l := lease(t, f)
	got := l.Event
	if got.ID == "" || got.IdemKey != e.IdemKey || got.Source != e.Source || got.Conv != e.Conv ||
		got.Kind != e.Kind || string(got.Payload) != string(e.Payload) || got.TTL != e.TTL || !got.At.Equal(e.At) {
		t.Fatalf("leased event = %+v, want %+v", got, e)
	}
	if l.Token == "" || !l.Until.Equal(t0.Add(time.Minute)) {
		t.Fatalf("lease token %q until %v", l.Token, l.Until)
	}
}

func TestLeaseExpiryRedelivers(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"))
	first := lease(t, f)
	f.clock.Advance(time.Minute + time.Second)
	second := lease(t, f)
	if second.Event.ID != first.Event.ID {
		t.Fatalf("re-lease gave %s, want %s", second.Event.ID, first.Event.ID)
	}
	if second.Token == first.Token {
		t.Fatal("re-lease kept the old token")
	}
	if n := count(t, f, `SELECT attempts FROM inbox WHERE idem_key = 'k1'`); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

func TestCompleteWithStaleTokenIsLeaseLost(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:1", "k1"))
	first := lease(t, f)
	f.clock.Advance(2 * time.Minute)
	_ = lease(t, f)

	err := f.q.Complete(ctx, first, []event.Outgoing{reply("telegram:1", "k1:reply:0")}, queue.StateUpdate{})
	if !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("Complete with stale token: %v, want ErrLeaseLost", err)
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("outbox rows = %d, want 0", n)
	}
}

func TestCompleteAfterDoneIsLeaseLost(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:1", "k1"))
	l := lease(t, f)
	if err := f.q.Complete(ctx, l, nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Complete(ctx, l, nil, queue.StateUpdate{}); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("second Complete: %v, want ErrLeaseLost", err)
	}
}

func TestCompleteExpiredButUntakenLeaseSucceeds(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"))
	l := lease(t, f)
	f.clock.Advance(time.Hour)
	if err := f.q.Complete(context.Background(), l, nil, queue.StateUpdate{}); err != nil {
		t.Fatalf("Complete after expiry, nobody re-leased: %v", err)
	}
}

func TestCompleteDuplicateReplyIgnored(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k2"))
	l := lease(t, f)
	if err := f.q.Complete(ctx, l, []event.Outgoing{reply("telegram:1", "dup")}, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	l = lease(t, f)
	out := []event.Outgoing{reply("telegram:1", "dup"), reply("telegram:1", "k2:reply:0")}
	if err := f.q.Complete(ctx, l, out, queue.StateUpdate{}); err != nil {
		t.Fatalf("Complete with a duplicate reply: %v", err)
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 2 {
		t.Fatalf("outbox rows = %d, want 2", n)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE status = 'done'`); n != 2 {
		t.Fatalf("done events = %d, want 2", n)
	}
}

func TestCompleteStoresOutgoing(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"))
	l := lease(t, f)
	r := reply("telegram:1", "k1:reply:0")
	r.Silent = true
	if err := f.q.Complete(context.Background(), l, []event.Outgoing{r}, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	var id, body string
	if err := f.db.QueryRow(`SELECT id, body FROM outbox WHERE idem_key = ?`, r.IdemKey).Scan(&id, &body); err != nil {
		t.Fatal(err)
	}
	var got event.Outgoing
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if id == "" || got.ID != id || got.Text != r.Text || !got.Silent || got.Conv != r.Conv || got.Transport != r.Transport {
		t.Fatalf("stored outgoing id %q body %+v", id, got)
	}
}

func TestCompleteIsAtomic(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:1", "k1"))
	l := lease(t, f)
	bad := queue.StateUpdate{Conv: "telegram:1", Data: json.RawMessage(`{not json`)}
	if err := f.q.Complete(ctx, l, []event.Outgoing{reply("telegram:1", "k1:reply:0")}, bad); err == nil {
		t.Fatal("Complete with invalid state succeeded")
	}
	if n := count(t, f, `SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("outbox rows = %d, want 0", n)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE status = 'pending' AND lease_token = ?`, l.Token); n != 1 {
		t.Fatal("event is no longer pending under its lease")
	}
	// The lease is still held: a retry with valid state completes.
	good := queue.StateUpdate{Conv: "telegram:1", Data: json.RawMessage(`{}`)}
	if err := f.q.Complete(ctx, l, nil, good); err != nil {
		t.Fatalf("retry after failed Complete: %v", err)
	}
}

func TestCompleteWritesState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if st, err := f.q.State(ctx, "telegram:1"); err != nil || st != nil {
		t.Fatalf("State before any turn = %s, %v; want nil, nil", st, err)
	}
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k2"))
	for i, data := range []string{`{"turn":1}`, `{"turn":2}`} {
		l := lease(t, f)
		if err := f.q.Complete(ctx, l, nil, queue.StateUpdate{Conv: "telegram:1", Data: json.RawMessage(data)}); err != nil {
			t.Fatalf("Complete %d: %v", i, err)
		}
		st, err := f.q.State(ctx, "telegram:1")
		if err != nil || string(st) != data {
			t.Fatalf("State after turn %d = %s, %v; want %s", i+1, st, err, data)
		}
	}
}

func TestExpiredEventIsSkipped(t *testing.T) {
	f := setup(t)
	short := msg("telegram:1", "k1")
	short.TTL = time.Second
	put(t, f, short, msg("telegram:1", "k2"))
	f.clock.Advance(2 * time.Second)
	if l := lease(t, f); l.Event.IdemKey != "k2" {
		t.Fatalf("lease = %s, want k2", l.Event.IdemKey)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE idem_key = 'k1' AND status = 'expired'`); n != 1 {
		t.Fatal("expired event not marked expired")
	}
}

func TestReopenKeepsQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := &fakeClock{now: t0}
	f := openAt(t, path, clock)
	put(t, f, msg("telegram:1", "k1"))
	first := lease(t, f)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}

	f = openAt(t, path, clock)
	if _, err := f.q.Lease(context.Background(), time.Minute); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("lease while the old lease runs: %v, want ErrEmpty", err)
	}
	clock.Advance(time.Minute + time.Millisecond)
	if l := lease(t, f); l.Event.ID != first.Event.ID {
		t.Fatalf("after reopen got %s, want %s", l.Event.ID, first.Event.ID)
	}
}

func TestInboxReadySignals(t *testing.T) {
	f := setup(t)
	ready := f.q.InboxReady()
	select {
	case <-ready:
		t.Fatal("signalled before any Put")
	default:
	}
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k2")) // two Puts never block
	select {
	case <-ready:
	default:
		t.Fatal("no signal after Put")
	}
	put(t, f, msg("telegram:1", "k1"))
	select {
	case <-ready:
		t.Fatal("signalled after a duplicate Put")
	default:
	}
}

func TestOutboxReadySignalsAfterComplete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k2"))
	if err := f.q.Complete(ctx, lease(t, f), nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.q.OutboxReady():
		t.Fatal("signalled after a Complete with no replies")
	default:
	}
	if err := f.q.Complete(ctx, lease(t, f), []event.Outgoing{reply("telegram:1", "k2:reply:0")}, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.q.OutboxReady():
	default:
		t.Fatal("no signal after a Complete that stored a reply")
	}
}

func TestPrune(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const retention = 7 * 24 * time.Hour

	// Old rows: one done, one expired, one pending event; one sent, one
	// failed, one pending reply.
	short := msg("telegram:x", "old-expired")
	short.TTL = time.Second
	put(t, f, msg("telegram:1", "old-done"), short, msg("telegram:2", "old-pending"))
	f.clock.Advance(2 * time.Second)
	l := lease(t, f) // old-done heads the queue
	if l.Event.IdemKey != "old-done" {
		t.Fatalf("lease = %s, want old-done", l.Event.IdemKey)
	}
	if _, err := f.q.Lease(ctx, time.Minute); err != nil { // old-expired gets marked, old-pending leased
		t.Fatal(err)
	}
	out := []event.Outgoing{reply("telegram:1", "r-sent"), reply("telegram:3", "r-failed"), reply("telegram:4", "r-pending")}
	if err := f.q.Complete(ctx, l, out, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	now := f.clock.Now().UnixMilli()
	if _, err := f.db.Exec(`UPDATE outbox SET status = 'sent', done_ms = ? WHERE idem_key = 'r-sent'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE outbox SET status = 'failed', done_ms = ? WHERE idem_key = 'r-failed'`, now); err != nil {
		t.Fatal(err)
	}

	// Recent rows: done the day before the prune.
	f.clock.Advance(retention)
	put(t, f, msg("telegram:5", "new-done"))
	l = lease(t, f)
	if err := f.q.Complete(ctx, l, []event.Outgoing{reply("telegram:5", "r-new-sent")}, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE outbox SET status = 'sent', done_ms = ? WHERE idem_key = 'r-new-sent'`, f.clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(24 * time.Hour)
	n, err := f.q.Prune(ctx, retention, sqlqueue.RefRetention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("Prune removed %d rows, want 4", n)
	}
	for _, key := range []string{"old-pending", "new-done"} {
		if count(t, f, `SELECT count(*) FROM inbox WHERE idem_key = ?`, key) != 1 {
			t.Errorf("inbox %s was pruned", key)
		}
	}
	for _, key := range []string{"r-pending", "r-new-sent"} {
		if count(t, f, `SELECT count(*) FROM outbox WHERE idem_key = ?`, key) != 1 {
			t.Errorf("outbox %s was pruned", key)
		}
	}
	if count(t, f, `SELECT count(*) FROM inbox`) != 2 || count(t, f, `SELECT count(*) FROM outbox`) != 2 {
		t.Error("old done/expired/sent/failed rows survived")
	}
}

func TestConcurrentLeasesNeverShareAnEvent(t *testing.T) {
	f := setup(t)
	const n = 40
	for i := range n {
		conv := "telegram:" + string(rune('a'+i%8))
		put(t, f, msg(conv, conv+"/"+string(rune('0'+i/8))))
	}
	var (
		mu   sync.Mutex
		seen = map[string]int{}
		wg   sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			for {
				l, err := f.q.Lease(context.Background(), time.Minute)
				if errors.Is(err, queue.ErrEmpty) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seen[l.Event.ID]++
				mu.Unlock()
				if err := f.q.Complete(context.Background(), l, nil, queue.StateUpdate{}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	for id, k := range seen {
		if k != 1 {
			t.Errorf("event %s leased %d times", id, k)
		}
	}
	if done := count(t, f, `SELECT count(*) FROM inbox WHERE status = 'done'`); done != n {
		t.Errorf("done events = %d, want %d", done, n)
	}
}

func TestReleaseLeasesAfterCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := &fakeClock{now: t0}
	f := openAt(t, path, clock)
	put(t, f, msg("telegram:1", "k1"), msg("telegram:1", "k2"), msg("telegram:2", "k3"))
	ghost := lease(t, f)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}

	f = openAt(t, path, clock)
	ctx := context.Background()
	n, err := f.q.ReleaseLeases(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ReleaseLeases = %d, %v; want 1", n, err)
	}
	if l := lease(t, f); l.Event.ID != ghost.Event.ID {
		t.Fatalf("after release got %s, want the released %s at once", l.Event.IdemKey, ghost.Event.IdemKey)
	}
	if err := f.q.Complete(ctx, ghost, nil, queue.StateUpdate{}); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("the dead holder's Complete = %v, want ErrLeaseLost", err)
	}
	if n, err := f.q.ReleaseLeases(ctx); err != nil || n != 1 {
		t.Fatalf("second ReleaseLeases = %d, %v; want 1 (the new lease)", n, err)
	}
}

// ADR-0046: a restart is no failure of the event. A message at the head of
// its conversation through five restarts keeps its attempts.
func TestReleaseCountsAnInterruptionNotAnAttempt(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"))
	for i := range 5 {
		if l := lease(t, f); l.Attempts != 1 || l.Interrupted != i {
			t.Fatalf("run %d: attempts %d, interrupted %d", i, l.Attempts, l.Interrupted)
		}
		if _, err := f.q.ReleaseLeases(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLeaseAttempts(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:1", "k1"))
	if l := lease(t, f); l.Attempts != 1 {
		t.Fatalf("first lease attempts %d", l.Attempts)
	}
	f.clock.Advance(2 * time.Minute)
	if l := lease(t, f); l.Attempts != 2 {
		t.Fatalf("second lease attempts %d", l.Attempts)
	}
}

// behindKeys is the IdemKeys of what waits behind e.
func behindKeys(t *testing.T, f fixture, e event.Event, limit int) []string {
	t.Helper()
	es, err := f.q.Behind(context.Background(), e, limit)
	if err != nil {
		t.Fatalf("Behind: %v", err)
	}
	keys := []string{}
	for _, b := range es {
		keys = append(keys, b.IdemKey)
	}
	return keys
}

func TestBehindListsOnlyLaterEventsOfTheConversation(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"), msg("telegram:b", "b1"), msg("telegram:a", "a3"))
	l := lease(t, f)
	if got := behindKeys(t, f, l.Event, 10); !slices.Equal(got, []string{"a2", "a3"}) {
		t.Fatalf("Behind(a1, 10) = %v", got)
	}
	if got := behindKeys(t, f, l.Event, 1); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("Behind(a1, 1) = %v", got)
	}
	es, err := f.q.Behind(context.Background(), l.Event, 10)
	if err != nil {
		t.Fatal(err)
	}
	if string(es[0].Payload) != string(msg("telegram:a", "a2").Payload) || es[0].Kind != "message" || es[0].Conv != "telegram:a" || !es[0].At.Equal(t0) {
		t.Fatalf("a2 came back as %+v", es[0])
	}
	if got := behindKeys(t, f, es[1], 10); len(got) != 0 {
		t.Fatalf("Behind(a3, 10) = %v", got)
	}
}

func TestBehindOfAnUnknownEventIsEmpty(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"))
	if got := behindKeys(t, f, event.Event{ID: "nope", Conv: "telegram:a"}, 10); len(got) != 0 {
		t.Fatalf("Behind(unknown) = %v", got)
	}
}

func TestBehindSkipsFinishedEvents(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"), msg("telegram:a", "a3"))
	l := lease(t, f)
	es, err := f.q.Behind(context.Background(), l.Event, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.Complete(context.Background(), l, nil, queue.StateUpdate{Taken: []string{es[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if got := behindKeys(t, f, l.Event, 10); !slices.Equal(got, []string{"a3"}) {
		t.Fatalf("Behind after a2 was taken = %v", got)
	}
}

func TestCompleteMarksTakenEventsDoneInOneTransaction(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"), msg("telegram:a", "a3"))
	l := lease(t, f)
	es, err := f.q.Behind(context.Background(), l.Event, 1)
	if err != nil {
		t.Fatal(err)
	}
	st := queue.StateUpdate{Conv: "telegram:a", Data: json.RawMessage(`{}`), Taken: []string{es[0].ID}}
	if err := f.q.Complete(context.Background(), l, nil, st); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE idem_key = 'a2' AND status = 'done' AND done_ms > 0 AND taken_by = ?`, l.Event.ID); n != 1 {
		t.Fatalf("a2 done and taken by a1: %d rows", n)
	}
	if next := lease(t, f); next.Event.IdemKey != "a3" {
		t.Fatalf("next lease = %s, want a3", next.Event.IdemKey)
	}
}

// A turn that lost its lease has answered nothing, so what it took stays
// to be answered: the holder that finished the event took nothing.
func TestFailedCompleteLeavesTakenEventsPending(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:a", "a2"))
	stale := lease(t, f)
	es, err := f.q.Behind(context.Background(), stale.Event, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Minute)
	if err := f.q.Complete(context.Background(), lease(t, f), nil, queue.StateUpdate{}); err != nil {
		t.Fatal(err)
	}
	err = f.q.Complete(context.Background(), stale, nil, queue.StateUpdate{Taken: []string{es[0].ID}})
	if !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("stale Complete = %v, want ErrLeaseLost", err)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE idem_key = 'a2' AND status = 'pending' AND taken_by IS NULL`); n != 1 {
		t.Fatalf("a2 pending and untaken: %d rows", n)
	}
	if next := lease(t, f); next.Event.IdemKey != "a2" {
		t.Fatalf("next lease = %s, want a2", next.Event.IdemKey)
	}
}

func TestCompleteIgnoresTakenFromAnotherConversation(t *testing.T) {
	f := setup(t)
	put(t, f, msg("telegram:a", "a1"), msg("telegram:b", "b1"))
	l := lease(t, f)
	var b1 string
	if err := f.db.QueryRowContext(context.Background(), `SELECT id FROM inbox WHERE idem_key = 'b1'`).Scan(&b1); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Complete(context.Background(), l, nil, queue.StateUpdate{Taken: []string{b1}}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f, `SELECT count(*) FROM inbox WHERE idem_key = 'b1' AND status = 'pending' AND taken_by IS NULL`); n != 1 {
		t.Fatalf("b1 pending and untaken: %d rows", n)
	}
}

// R-DLV-14: a message stored with no origin under a context that carries one gets
// it; one with its own keeps it.
func TestEnqueueStampsTheContextsOrigin(t *testing.T) {
	f := setup(t)
	own := event.Origin{Kind: event.OriginReminder, Session: "s0"}
	ctx := event.WithOrigin(context.Background(), func() event.Origin {
		return event.Origin{Kind: event.OriginTurn, Session: "s1", Tainted: true}
	})
	bare, kept := reply("telegram:1", "a"), reply("telegram:1", "b")
	kept.Origin = &own
	if err := f.q.Enqueue(ctx, bare, kept); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]event.Origin{"a": {Kind: event.OriginTurn, Session: "s1", Tainted: true}, "b": own} {
		var body string
		if err := f.db.QueryRow(`SELECT body FROM outbox WHERE idem_key = ?`, key).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var got event.Outgoing
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if got.Origin == nil || *got.Origin != want {
			t.Errorf("%s: origin %+v, want %+v", key, got.Origin, want)
		}
	}
}
