package outbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// vclock is virtual time. The sender reports each wait through waits and
// blocks until the test takes it, so between two waits the test sees the
// state after a whole pass. The test then moves time with advance.
type vclock struct {
	mu     sync.Mutex
	now    time.Time
	timers []vtimer
	waits  chan time.Time
	stop   chan struct{}
}

type vtimer struct {
	at time.Time
	c  chan time.Time
}

func newVClock() *vclock {
	return &vclock{now: t0, waits: make(chan time.Time), stop: make(chan struct{})}
}

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *vclock) after(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	t := vtimer{at: c.now.Add(d), c: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	select {
	case c.waits <- t.at:
	case <-c.stop:
	}
	return t.c
}

// wait returns the deadline of the sender's next wait.
func (c *vclock) wait(t *testing.T) time.Time {
	t.Helper()
	select {
	case at := <-c.waits:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not wait")
		return time.Time{}
	}
}

// advance moves time to at and fires the timers due by then.
func (c *vclock) advance(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at.After(c.now) {
		c.now = at
	}
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.at.After(c.now) {
			kept = append(kept, t)
			continue
		}
		t.c <- t.at
	}
	c.timers = kept
}

type sendRecord struct {
	key string
	at  time.Time
	err error
}

// fakeTransport records sends; script decides the result of each send.
type fakeTransport struct {
	name   string
	clock  *vclock
	script func(msg event.Outgoing, n int) error // n counts sends of this IdemKey from 0
	mu     sync.Mutex
	sends  []sendRecord
	tries  map[string]int
	refs   map[string]string // what Send returns per IdemKey
}

func newFake(clock *vclock, script func(event.Outgoing, int) error) *fakeTransport {
	return &fakeTransport{name: "telegram", clock: clock, script: script, tries: map[string]int{}}
}

func (f *fakeTransport) Name() string                                    { return f.name }
func (f *fakeTransport) Run(ctx context.Context, _ transport.Sink) error { <-ctx.Done(); return nil }

func (f *fakeTransport) Send(_ context.Context, msg event.Outgoing) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.tries[msg.IdemKey]
	f.tries[msg.IdemKey]++
	var err error
	if f.script != nil {
		err = f.script(msg, n)
	}
	f.sends = append(f.sends, sendRecord{key: msg.IdemKey, at: f.clock.Now(), err: err})
	return f.refs[msg.IdemKey], err
}

func (f *fakeTransport) records() []sendRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sends)
}

// delivered lists the keys sent successfully, in order.
func (f *fakeTransport) delivered() []string {
	var out []string
	for _, r := range f.records() {
		if r.err == nil {
			out = append(out, r.key)
		}
	}
	return out
}

// partFake splits every message into three parts.
type partFake struct{ *fakeTransport }

func (p partFake) Parts(msg event.Outgoing) []event.Outgoing {
	var out []event.Outgoing
	for i := range 3 {
		part := msg
		part.IdemKey = fmt.Sprintf("%s:p%d", msg.IdemKey, i)
		part.Text = fmt.Sprintf("%s (%d/3)", msg.Text, i+1)
		out = append(out, part)
	}
	return out
}

type env struct {
	q     *sqlqueue.Queue
	clock *vclock
	path  string
}

func newEnv(t *testing.T) *env {
	return openEnv(t, filepath.Join(t.TempDir(), "queue.db"), newVClock())
}

func openEnv(t *testing.T, path string, clock *vclock) *env {
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
	return &env{q: q, clock: clock, path: path}
}

func out(conv, key string) event.Outgoing {
	return event.Outgoing{IdemKey: key, Transport: "telegram", Conv: conv, Text: "secret family text"}
}

func (e *env) enqueue(t *testing.T, msgs ...event.Outgoing) {
	t.Helper()
	if err := e.q.Enqueue(context.Background(), msgs...); err != nil {
		t.Fatal(err)
	}
}

// start runs a sender with tr until the test ends or stop is called.
func (e *env) start(t *testing.T, tr transport.Transport, opts ...Option) (stop func()) {
	t.Helper()
	opts = append([]Option{WithClock(e.clock.Now), withAfter(e.clock.after)}, opts...)
	s := NewSender(e.q, e.q.OutboxReady(), slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	s.Register(tr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}) // closed, not sent on: two goroutines wait on it
	go func() {
		defer close(done)
		if err := s.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			// Release a sender blocked on reporting a wait.
			go func() {
				for {
					select {
					case <-e.clock.waits:
					case <-done:
						return
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("sender did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// runUntil lets virtual time run, wait after wait, until cond holds or
// limit passes.
func (e *env) runUntil(t *testing.T, limit time.Time, cond func() bool) {
	t.Helper()
	for {
		at := e.clock.wait(t)
		if cond() {
			return
		}
		if at.After(limit) {
			t.Fatalf("condition not met by %v", limit)
		}
		e.clock.advance(at)
	}
}

func TestSendsInOrderPerConversation(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, out("telegram:a", "a1"), out("telegram:a", "a2"), out("telegram:a", "a3"),
		out("telegram:b", "b1"), out("telegram:b", "b2"))
	tr := newFake(e.clock, func(m event.Outgoing, n int) error {
		if m.IdemKey == "a2" && n < 2 {
			return errors.New("network down")
		}
		return nil
	})
	e.start(t, tr, WithBackoff(func(int) time.Duration { return 10 * time.Second }))
	e.runUntil(t, t0.Add(time.Hour), func() bool { return len(tr.delivered()) == 5 })

	var a, b []string
	for _, k := range tr.delivered() {
		if k[0] == 'a' {
			a = append(a, k)
		} else {
			b = append(b, k)
		}
	}
	if !slices.Equal(a, []string{"a1", "a2", "a3"}) || !slices.Equal(b, []string{"b1", "b2"}) {
		t.Fatalf("delivered a=%v b=%v", a, b)
	}
	for _, r := range tr.records() {
		if r.key[0] == 'b' && !r.at.Equal(t0) {
			t.Errorf("%s sent at %v: conversation b was delayed by a", r.key, r.at)
		}
	}
	if got := len(tr.records()); got != 7 {
		t.Errorf("send calls = %d, want 7 (two failures of a2)", got)
	}
}

func TestRetryAfterPausesTransport(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, out("telegram:a", "a1"), out("telegram:b", "b1"))
	tr := newFake(e.clock, func(m event.Outgoing, n int) error {
		if m.IdemKey == "a1" && n == 0 {
			return &transport.RetryAfterError{After: 3 * time.Second}
		}
		return nil
	})
	e.start(t, tr)
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(tr.delivered()) == 2 })

	recs := tr.records()
	if len(recs) != 3 {
		t.Fatalf("sends = %+v, want 3", recs)
	}
	for _, r := range recs[1:] {
		if r.at.Before(t0.Add(3 * time.Second)) {
			t.Errorf("%s sent at %v, during the 3 s pause", r.key, r.at)
		}
	}
	db, err := sqlitedb.Open(context.Background(), e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var attempts int
	if err := db.QueryRow(`SELECT attempts FROM outbox WHERE idem_key = 'a1'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("a1 attempts = %d (%v), want 0: retry_after is not a failed attempt", attempts, err)
	}
}

func TestPermanentErrorFails(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, out("telegram:a", "a1"), out("telegram:a", "a2"))
	tr := newFake(e.clock, func(m event.Outgoing, _ int) error {
		if m.IdemKey == "a1" {
			return fmt.Errorf("chat not found: %w", transport.ErrPermanent)
		}
		return nil
	})
	e.start(t, tr)
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(tr.records()) == 2 })
	if got := tr.delivered(); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("delivered %v, want [a2]", got)
	}
	if recs := tr.records(); !recs[1].at.Equal(t0) {
		t.Errorf("a2 waited until %v after a permanent failure", recs[1].at)
	}
}

func TestUnknownTransportWaits(t *testing.T) {
	e := newEnv(t)
	other := out("telegram:a", "x1")
	other.Transport = "carrier-pigeon"
	e.enqueue(t, other, out("telegram:a", "a1"))
	tr := newFake(e.clock, nil)
	e.start(t, tr)
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(tr.delivered()) == 1 })
	// One more idle cycle: the unknown transport's message must still wait.
	e.clock.advance(e.clock.wait(t))
	e.clock.wait(t)
	ps, _, err := e.q.Due(context.Background(), 10)
	if err != nil || len(ps) != 1 || ps[0].Msg.IdemKey != "x1" || ps[0].Attempts != 0 {
		t.Fatalf("due = %+v, %v; want x1 still pending, untouched", ps, err)
	}
}

func TestOutageBacksOffWithoutBusyLoop(t *testing.T) {
	e := newEnv(t)
	e.enqueue(t, out("telegram:a", "a1"), out("telegram:a", "a2"), out("telegram:a", "a3"))
	recovery := t0.Add(6 * time.Hour)
	tr := newFake(e.clock, func(event.Outgoing, int) error {
		if e.clock.Now().Before(recovery) {
			return errors.New("dial tcp: connection refused")
		}
		return nil
	})
	e.start(t, tr)
	e.runUntil(t, t0.Add(7*time.Hour), func() bool { return len(tr.delivered()) == 3 })

	recs := tr.records()
	if len(recs) >= 100 {
		t.Fatalf("send calls = %d during a 6 h outage, want < 100", len(recs))
	}
	if got := tr.delivered(); !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("delivered %v", got)
	}
	var gaps []time.Duration
	for i := 1; i < len(recs) && recs[i].err != nil; i++ {
		gaps = append(gaps, recs[i].at.Sub(recs[i-1].at))
	}
	if len(gaps) < 10 {
		t.Fatalf("only %d gaps", len(gaps))
	}
	if gaps[0] > 2*time.Second {
		t.Errorf("first gap %v, want about 1 s", gaps[0])
	}
	for _, g := range gaps[len(gaps)-10:] {
		if g < 4*time.Minute || g > 5*time.Minute+30*time.Second {
			t.Errorf("late gap %v, want near the 5 min cap", g)
		}
	}
	if last := recs[len(recs)-3]; last.at.Sub(recovery) > 6*time.Minute {
		t.Errorf("first delivery at %v, %v after recovery", last.at, last.at.Sub(recovery))
	}
}

func TestSplitMessageResumesAfterCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := newVClock()
	e := openEnv(t, path, clock)
	e.enqueue(t, out("telegram:a", "a1"))

	broken := partFake{newFake(clock, func(m event.Outgoing, _ int) error {
		if m.IdemKey == "a1:p1" {
			return errors.New("timeout")
		}
		return nil
	})}
	stop := e.start(t, broken)
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(broken.records()) == 2 })
	stop()
	if got := broken.delivered(); !slices.Equal(got, []string{"a1:p0"}) {
		t.Fatalf("first sender delivered %v", got)
	}

	// A new sender on the same file, as after a crash.
	clock2 := newVClock()
	clock2.now = clock.Now()
	e2 := openEnv(t, path, clock2)
	good := partFake{newFake(clock2, nil)}
	e2.start(t, good)
	e2.runUntil(t, t0.Add(time.Hour), func() bool { return len(good.delivered()) == 2 })
	if got := good.delivered(); !slices.Equal(got, []string{"a1:p1", "a1:p2"}) {
		t.Fatalf("second sender delivered %v, want [a1:p1 a1:p2]", got)
	}
	if ps, _, err := e2.q.Due(context.Background(), 10); err != nil || len(ps) != 0 {
		t.Fatalf("message still due after all parts: %+v, %v", ps, err)
	}
}

func TestSenderRecordsTheRefOfTheButtons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	clock := newVClock()
	e := openEnv(t, path, clock)
	e.enqueue(t, out("telegram:a", "a1"))
	f := newFake(clock, nil)
	f.refs = map[string]string{"a1:p0": "1", "a1:p1": "2", "a1:p2": "3"}
	e.start(t, partFake{f})
	e.runUntil(t, t0.Add(time.Minute), func() bool {
		ref, err := e.q.SentRef(context.Background(), "a1")
		return err == nil && ref != ""
	})
	if ref, err := e.q.SentRef(context.Background(), "a1"); err != nil || ref != "3" {
		t.Fatalf("ref %q, %v; want the last part's, which holds the buttons", ref, err)
	}
}

func TestWakeTriggersImmediateSend(t *testing.T) {
	e := newEnv(t)
	tr := newFake(e.clock, nil)
	e.start(t, tr)
	if at := e.clock.wait(t); !at.Equal(t0.Add(DefaultPoll)) {
		t.Fatalf("idle wait until %v, want the poll interval", at)
	}
	e.enqueue(t, out("telegram:a", "a1"))
	e.clock.wait(t)
	if got := tr.records(); len(got) != 1 || !got[0].at.Equal(t0) {
		t.Fatalf("sends after wake = %+v, want a1 at t0", got)
	}
}

func TestDefaultBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempt  int
		min, max time.Duration
	}{
		{1, 800 * time.Millisecond, 1200 * time.Millisecond},
		{4, 6400 * time.Millisecond, 9600 * time.Millisecond},
		{10, 5 * time.Minute, 5 * time.Minute}, // 512 s ± 20% is above the cap
		{40, 4 * time.Minute, 5 * time.Minute},
	} {
		for range 50 {
			if d := DefaultBackoff(tc.attempt); d < tc.min || d > tc.max {
				t.Fatalf("DefaultBackoff(%d) = %v, want in [%v, %v]", tc.attempt, d, tc.min, tc.max)
			}
		}
	}
}

func TestSenderTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t)
	bad := out("telegram:c", "c1")
	e.enqueue(t, out("telegram:a", "a1"), out("telegram:b", "b1"), bad)
	tr := newFake(e.clock, func(m event.Outgoing, n int) error {
		switch {
		case m.IdemKey == "a1" && n == 0:
			return &transport.RetryAfterError{After: time.Second}
		case m.IdemKey == "b1" && n == 0:
			return errors.New("timeout")
		case m.IdemKey == "c1":
			return fmt.Errorf("blocked: %w", transport.ErrPermanent)
		}
		return nil
	})
	e.start(t, tr, WithBackoff(func(int) time.Duration { return time.Second }))
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(tr.delivered()) == 2 })
	for _, c := range []struct {
		name  string
		attrs map[string]string
		want  float64
	}{
		{"ganglion.outbox.sent", map[string]string{"transport": "telegram"}, 2},
		{"ganglion.outbox.retries", map[string]string{"transport": "telegram", "kind": "retry_after"}, 1},
		{"ganglion.outbox.retries", map[string]string{"transport": "telegram", "kind": "error"}, 1},
		{"ganglion.outbox.failed", map[string]string{"transport": "telegram"}, 1},
	} {
		if got := rec.Value(t, c.name, c.attrs); got != c.want {
			t.Errorf("%s %v = %v, want %v", c.name, c.attrs, got, c.want)
		}
	}
}

func TestAttachmentTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t)
	pic := out("telegram:a", "a1")
	pic.Attach = &event.Attachment{Kind: event.AttachPhoto, Name: "m.png", Type: "image/png", Data: make([]byte, 3000)}
	bad := out("telegram:b", "b1")
	bad.Attach = &event.Attachment{Kind: event.AttachDocument, Name: "r.txt", Type: "text/plain", Data: []byte("x")}
	e.enqueue(t, pic, bad)
	tr := newFake(e.clock, func(m event.Outgoing, _ int) error {
		if m.IdemKey == "b1" {
			return fmt.Errorf("refused: %w", transport.ErrPermanent)
		}
		return nil
	})
	e.start(t, tr)
	e.runUntil(t, t0.Add(time.Minute), func() bool { return len(tr.records()) == 2 })
	if got := rec.Value(t, "ganglion.outbox.attachments", map[string]string{"kind": "photo", "result": "sent"}); got != 1 {
		t.Errorf("photo sent = %v", got)
	}
	if got := rec.Value(t, "ganglion.outbox.attachments", map[string]string{"kind": "document", "result": "failed"}); got != 1 {
		t.Errorf("document failed = %v", got)
	}
	if got := rec.Sum(t, "ganglion.outbox.attachment.bytes", map[string]string{"kind": "photo"}); got != 3000 {
		t.Errorf("photo bytes = %v", got)
	}
}
