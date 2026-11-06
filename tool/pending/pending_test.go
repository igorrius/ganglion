package pending_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

var _ policy.Asker = (*pending.Store)(nil)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

const owner = "telegram:user:42"

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	db    *sql.DB
	path  string
	clk   *clock
	cells *cells.Service
	s     *pending.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pending.db")
	f := &fixture{path: path, clk: &clock{now: t0}}
	f.open(t)
	return f
}

// open opens the database file and a store over it, as a node start does.
func (f *fixture) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cs, err := sqlstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	f.db, f.cells = db, cells.NewService(cs, cells.WithClock(f.clk.Now))
	f.s, err = pending.New(ctx, db, f.cells, f.clk.Now, pending.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
}

func call(n int) tool.Call {
	return tool.Call{TurnID: "01TURN", N: n, Conv: "telegram:42", Transport: "telegram", Principal: owner, Admin: true,
		Name: "web.fetch", Args: json.RawMessage(`{"url":"https://example.com/x"}`)}
}

// ask parks call n and returns its id and the question with its buttons.
func (f *fixture) ask(t *testing.T, n int) (string, event.Outgoing) {
	t.Helper()
	ctx := context.Background()
	id, err := f.s.Ask(ctx, call(n), "Run web.fetch with {...}?")
	if err != nil || id == "" {
		t.Fatalf("Ask = %q, %v", id, err)
	}
	q, err := f.s.Outgoing(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, q
}

func by(event string) pending.By {
	return pending.By{Principal: owner, Conv: "telegram:42", Event: event}
}

func TestAskReturnsTheQuestionWithButtons(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 2)
	if q.IdemKey != "01TURN:2:confirm" || q.Transport != "telegram" || q.Conv != "telegram:42" || q.Text != "Run web.fetch with {...}?" {
		t.Fatalf("question %+v", q)
	}
	if len(q.Buttons) != 2 || q.Buttons[0].Label != "✅ Approve" || q.Buttons[1].Label != "❌ Deny" {
		t.Fatalf("buttons %+v", q.Buttons)
	}
	for _, b := range q.Buttons {
		if len(b.ID) != 26 || strings.Contains(b.ID, "web") || b.ID == id {
			t.Errorf("button id %q is not a random one-time id", b.ID)
		}
	}
	it, err := f.s.Get(context.Background(), id)
	if err != nil || it.Status != pending.StatusWaiting || it.Tool != "web.fetch" || it.TurnID != "01TURN" || it.N != 2 ||
		it.Principal != owner || !it.Expires.Equal(t0.Add(pending.DefaultTTL)) || string(it.Args) != `{"url":"https://example.com/x"}` {
		t.Fatalf("item %+v, %v", it, err)
	}
	if c := it.Call(); c.Key() != "01TURN:2" || c.Name != "web.fetch" || c.Conv != "telegram:42" || c.Transport != "telegram" {
		t.Fatalf("rebuilt call %+v", c)
	}
}

// A turn that reruns after a crash asks again with the same call key and
// gets the same pending call, not a second one.
func TestAskIsIdempotentByCallKey(t *testing.T) {
	f := newFixture(t)
	a, qa := f.ask(t, 1)
	b, qb := f.ask(t, 1)
	if a != b || qa.Buttons[0].ID != qb.Buttons[0].ID {
		t.Fatalf("rerun gave %s (%v), first %s (%v)", b, qb.Buttons, a, qa.Buttons)
	}
	c, _ := f.ask(t, 2)
	if c == a {
		t.Fatal("another call got the same id")
	}
}

func TestAskCreatesRequestCell(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	ctx := context.Background()
	c, err := f.cells.Get(ctx, "request.call."+strings.ToLower(id))
	if err != nil {
		t.Fatal(err)
	}
	d, err := cells.DecisionOf(c)
	if err != nil || d.Status != cells.StatusPending || !c.Expires.Equal(t0.Add(pending.DefaultTTL)) {
		t.Fatalf("cell %+v, decision %+v, %v", c, d, err)
	}
	var r cells.Request
	if err := json.Unmarshal(c.Desired, &r); err != nil || r.Question != q.Text {
		t.Fatalf("request %s", c.Desired)
	}
	if _, err := f.s.Press(ctx, q.Buttons[0].ID, by("ev1")); err != nil {
		t.Fatal(err)
	}
	c, _ = f.cells.Get(ctx, "request.call."+strings.ToLower(id))
	if d, _ := cells.DecisionOf(c); d.Status != cells.StatusApproved || d.By != owner {
		t.Fatalf("after approve the cell says %+v", d)
	}
}

func TestPressOnce(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	presses := make([]pending.Press, 2)
	for i := range 2 {
		wg.Go(func() { presses[i], errs[i] = f.s.Press(ctx, q.Buttons[0].ID, by("ev"+string(rune('a'+i)))) })
	}
	wg.Wait()
	ok, used := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
			if presses[i].Item.ID != id || presses[i].Item.Status != pending.StatusApproved || presses[i].Button.Action != pending.ActionApprove {
				t.Errorf("press %+v", presses[i])
			}
		case errors.Is(err, pending.ErrUsed):
			used++
		default:
			t.Errorf("press: %v", err)
		}
	}
	if ok != 1 || used != 1 {
		t.Fatalf("%d presses ran, %d used; want 1 and 1", ok, used)
	}
	// The other button of the question is spent too.
	if _, err := f.s.Press(ctx, q.Buttons[1].ID, by("ev3")); !errors.Is(err, pending.ErrUsed) {
		t.Fatalf("deny after approve: %v, want ErrUsed", err)
	}
}

// A press stores its time, so a later look at the call shows when it was answered,
// not when it was looked at (R-POL-9). A call nobody answered has none.
func TestPressKeepsItsTime(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	ctx := context.Background()
	if it, err := f.s.Get(ctx, id); err != nil || !it.Answered.IsZero() {
		t.Fatalf("before a press: %v, %v", it.Answered, err)
	}
	at := f.clk.now.Add(time.Minute)
	f.clk.now = at
	first, err := f.s.Press(ctx, q.Buttons[0].ID, by("ev1"))
	if err != nil || !first.Item.Answered.Equal(at) {
		t.Fatalf("press: %v, %v; want %v", first.Item.Answered, err, at)
	}
	f.clk.now = at.Add(time.Hour)
	used, err := f.s.Press(ctx, q.Buttons[1].ID, by("ev2"))
	if !errors.Is(err, pending.ErrUsed) || !used.Item.Answered.Equal(at) {
		t.Fatalf("second press: %v, %v; want %v", used.Item.Answered, err, at)
	}
}

// The same event pressing again (its turn reruns after a crash) gets the
// same press back, so the rerun can finish what the first run started.
func TestPressRerunOfTheSameEvent(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	ctx := context.Background()
	first, err := f.s.Press(ctx, q.Buttons[1].ID, by("ev1"))
	if err != nil || first.Item.Status != pending.StatusDenied {
		t.Fatalf("deny = %+v, %v", first, err)
	}
	if err := f.s.Decide(ctx, id, pending.StatusDone, "the owner declined"); err != nil {
		t.Fatal(err)
	}
	again, err := f.s.Press(ctx, q.Buttons[1].ID, by("ev1"))
	if err != nil || again.Item.ID != id || again.Item.Status != pending.StatusDone || again.Item.Result != "the owner declined" {
		t.Fatalf("rerun = %+v, %v", again, err)
	}
}

func TestPressNotYours(t *testing.T) {
	f := newFixture(t)
	_, q := f.ask(t, 1)
	ctx := context.Background()
	for name, b := range map[string]pending.By{
		"another person": {Principal: "telegram:user:7", Conv: "telegram:42", Event: "ev1"},
		"another chat":   {Principal: owner, Conv: "telegram:-100", Event: "ev2"},
	} {
		p, err := f.s.Press(ctx, q.Buttons[0].ID, b)
		if !errors.Is(err, pending.ErrNotYours) || p.Item.ID != "" || p.Button.ID != "" {
			t.Errorf("%s: %+v, %v; want ErrNotYours and nothing", name, p, err)
		}
	}
	// The owner can still press it.
	if _, err := f.s.Press(ctx, q.Buttons[0].ID, by("ev3")); err != nil {
		t.Fatalf("owner after a stranger: %v", err)
	}
	if _, err := f.s.Press(ctx, "NOSUCHBUTTON00000000000000", by("ev4")); !errors.Is(err, pending.ErrNotFound) {
		t.Fatalf("unknown button: %v", err)
	}
}

func TestPressExpired(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	f.clk.add(pending.DefaultTTL)
	p, err := f.s.Press(context.Background(), q.Buttons[0].ID, by("ev1"))
	if !errors.Is(err, pending.ErrExpired) || p.Item.ID != id || p.Item.Question == "" {
		t.Fatalf("late press = %+v, %v; want ErrExpired with the item", p, err)
	}
	if it, _ := f.s.Get(context.Background(), id); it.Status != pending.StatusWaiting {
		t.Fatalf("a late press changed the item: %s", it.Status)
	}
}

// A button nobody pressed in time says Expired, also after the expiry
// reached the conversation (the call is done then), not "already used"
// (found by the M3b acceptance, Task 12).
func TestPressAfterExpiryWasHandled(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	ctx := context.Background()
	f.clk.add(pending.DefaultTTL)
	if items, err := f.s.ExpireDue(ctx); err != nil || len(items) != 1 {
		t.Fatalf("ExpireDue = %+v, %v", items, err)
	}
	if err := f.s.Finish(ctx, id, pending.Outcome{Result: "the owner did not answer in time"}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Press(ctx, q.Buttons[0].ID, by("ev1"))
	if !errors.Is(err, pending.ErrExpired) || p.Item.ID != id {
		t.Fatalf("press after the expiry was handled = %+v, %v; want ErrExpired", p, err)
	}
}

// Everything is in SQLite: a button made before a restart works after it.
func TestPressAfterRestart(t *testing.T) {
	f := newFixture(t)
	id, q := f.ask(t, 1)
	f.open(t)
	p, err := f.s.Press(context.Background(), q.Buttons[0].ID, by("ev1"))
	if err != nil || p.Item.ID != id {
		t.Fatalf("press after restart = %+v, %v", p, err)
	}
}

func TestQuickActionButtons(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b := pending.Button{ID: pending.NewButtonID(), Action: "schedule.delete", Label: "Delete", Principal: owner, Conv: "telegram:42",
		Args: json.RawMessage(`{"job":"j1"}`), Expires: t0.Add(time.Hour)}
	if err := f.s.AddButtons(ctx, b); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Press(ctx, b.ID, by("ev1"))
	if err != nil || p.Item.ID != "" || p.Button.Action != "schedule.delete" || string(p.Button.Args) != `{"job":"j1"}` || p.Button.Label != "Delete" {
		t.Fatalf("quick press = %+v, %v", p, err)
	}
	if _, err := f.s.Press(ctx, b.ID, by("ev2")); !errors.Is(err, pending.ErrUsed) {
		t.Fatalf("second quick press: %v", err)
	}
	if err := f.s.AddButtons(ctx, pending.Button{ID: "x", Action: "", Principal: owner, Conv: "c", Expires: t0}); err == nil {
		t.Fatal("a button without an action was stored")
	}
}

func TestWaitingKeepsOnlyWaitingIDs(t *testing.T) {
	f := newFixture(t)
	a, qa := f.ask(t, 1)
	b, _ := f.ask(t, 2)
	ctx := context.Background()
	if _, err := f.s.Press(ctx, qa.Buttons[0].ID, by("ev1")); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Waiting(ctx, []string{a, "gone", b})
	if err != nil || len(got) != 1 || got[0] != b {
		t.Fatalf("Waiting = %v, %v; want [%s]", got, err, b)
	}
	if got, err := f.s.Waiting(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("Waiting(nil) = %v, %v", got, err)
	}
}

func TestExpireDue(t *testing.T) {
	f := newFixture(t)
	a, _ := f.ask(t, 1)
	f.clk.add(time.Minute)
	b, _ := f.ask(t, 2)
	ctx := context.Background()
	f.clk.add(pending.DefaultTTL - time.Minute)
	items, err := f.s.ExpireDue(ctx)
	if err != nil || len(items) != 1 || items[0].ID != a || items[0].Status != pending.StatusExpired {
		t.Fatalf("ExpireDue = %+v, %v", items, err)
	}
	c, _ := f.cells.Get(ctx, "request.call."+strings.ToLower(a))
	if d, _ := cells.DecisionOf(c); d.Status != cells.StatusExpired {
		t.Fatalf("cell %+v", d)
	}
	// Until its outcome reaches the conversation, it is offered again.
	if items, _ := f.s.ExpireDue(ctx); len(items) != 1 {
		t.Fatalf("second ExpireDue = %+v", items)
	}
	// A day after its expiry it is no longer offered: an event that failed
	// every attempt does not come back once the inbox forgets its key.
	f.clk.add(pending.Reoffer)
	if items, _ := f.s.ExpireDue(ctx); len(items) != 1 || items[0].ID != b {
		t.Fatalf("a day later %+v, want only the call that expired since", items)
	}
	if err := f.s.Decide(ctx, b, pending.StatusDone, "expired"); err != nil {
		t.Fatal(err)
	}
	if items, _ := f.s.ExpireDue(ctx); len(items) != 0 {
		t.Fatalf("done item offered again: %+v", items)
	}
}

func TestDecideUnknown(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Decide(context.Background(), "nope", pending.StatusDone, ""); !errors.Is(err, pending.ErrNotFound) {
		t.Fatalf("Decide(unknown) = %v", err)
	}
}

// putter records the events the expirer puts.
type putter struct {
	mu  sync.Mutex
	got []event.Event
	err error
}

func (p *putter) put(_ context.Context, e event.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.got = append(p.got, e)
	return nil
}

func (p *putter) events() []event.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]event.Event(nil), p.got...)
}

func TestExpireDueEmitsExpired(t *testing.T) {
	f := newFixture(t)
	id, _ := f.ask(t, 1)
	f.clk.add(pending.DefaultTTL)
	ticks := make(chan time.Time)
	var waits []time.Duration
	var mu sync.Mutex
	after := func(d time.Duration) <-chan time.Time { mu.Lock(); waits = append(waits, d); mu.Unlock(); return ticks }
	p := &putter{}
	x := pending.NewExpirer(f.s, p.put, slog.New(slog.DiscardHandler), pending.WithAfter(after))
	if x.Name() != "expire-pending" {
		t.Fatalf("name %q", x.Name())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- x.Run(ctx) }()
	// The first pass runs before the first wait; taking the second tick
	// means the second pass is done too. It puts the same event again,
	// which the inbox drops by its key.
	ticks <- t0
	ticks <- t0
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	evs := p.events()
	if len(evs) < 2 {
		t.Fatalf("events %+v", evs)
	}
	e := evs[0]
	var r event.ToolResult
	if err := json.Unmarshal(e.Payload, &r); err != nil || r.Pending != id {
		t.Fatalf("payload %s", e.Payload)
	}
	if e.Kind != event.KindToolResult || e.IdemKey != "pending:"+id+":expired" || e.Conv != "telegram:42" || e.Source != "telegram" {
		t.Fatalf("event %+v", e)
	}
	if evs[1].IdemKey != e.IdemKey {
		t.Fatalf("second pass key %s", evs[1].IdemKey)
	}
	mu.Lock()
	defer mu.Unlock()
	if waits[0] != 30*time.Second {
		t.Fatalf("tick %v, want 30s", waits[0])
	}
}

func TestNewButtonID(t *testing.T) {
	a, b := pending.NewButtonID(), pending.NewButtonID()
	if len(a) != 26 || a == b || len("c:"+a) > 64 {
		t.Fatalf("ids %q %q", a, b)
	}
}

func TestPressNeedsTheEvent(t *testing.T) {
	f := newFixture(t)
	_, q := f.ask(t, 1)
	if _, err := f.s.Press(context.Background(), q.Buttons[0].ID, pending.By{Principal: owner, Conv: "telegram:42"}); err == nil {
		t.Fatal("a press without an event was accepted")
	}
}

// A spent sibling is not "pressed by" anyone: no event can take it for a
// rerun of its own press.
func TestSpentSiblingIsNotARerun(t *testing.T) {
	f := newFixture(t)
	_, q := f.ask(t, 1)
	ctx := context.Background()
	if _, err := f.s.Press(ctx, q.Buttons[0].ID, by("ev1")); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"ev1", "spent", "-"} {
		if _, err := f.s.Press(ctx, q.Buttons[1].ID, by(ev)); !errors.Is(err, pending.ErrUsed) {
			t.Errorf("sibling pressed by %q: %v, want ErrUsed", ev, err)
		}
	}
}

// The whole outcome is stored, so a rerun can give back what the first
// run produced: the messages and whether the text is outside content.
func TestFinishStoresTheOutcome(t *testing.T) {
	f := newFixture(t)
	id, _ := f.ask(t, 1)
	ctx := context.Background()
	out := []event.Outgoing{{IdemKey: "01TURN:1:send", Transport: "telegram", Conv: "telegram:7", Text: "hi"}}
	if err := f.s.Finish(ctx, id, pending.Outcome{Result: "page", External: true, Out: out}); err != nil {
		t.Fatal(err)
	}
	it, err := f.s.Get(ctx, id)
	if err != nil || it.Status != pending.StatusDone || it.Result != "page" || !it.External || len(it.Out) != 1 || it.Out[0].IdemKey != "01TURN:1:send" {
		t.Fatalf("item %+v, %v", it, err)
	}
	if err := f.s.Finish(ctx, "nope", pending.Outcome{}); !errors.Is(err, pending.ErrNotFound) {
		t.Fatalf("Finish(unknown) = %v", err)
	}
}

// A parked call keeps its depth, and the rebuilt call carries it
// (R-AGT-4, ruling R33).
func TestAskKeepsTheDepth(t *testing.T) {
	f := newFixture(t)
	c := call(3)
	c.Depth = 2
	id, err := f.s.Ask(context.Background(), c, "Run it?")
	if err != nil {
		t.Fatal(err)
	}
	it, err := f.s.Get(context.Background(), id)
	if err != nil || it.Depth != 2 || it.Call().Depth != 2 {
		t.Fatalf("item %+v, %v", it, err)
	}
}

// Parked finds a waiting call identical to c (tool and canonical
// arguments) in the same conversation by the same person; the call's own
// key, another URL, another conversation, an answered and an expired call
// are not it (ruling R38).
func TestParkedFindsAWaitingIdenticalCall(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, q := f.ask(t, 1)
	later := call(1)
	later.TurnID, later.Args = "01LATER", json.RawMessage("{ \"url\" : \"https://example.com/x\" }")
	parked := func(c tool.Call) string {
		t.Helper()
		got, err := f.s.Parked(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := parked(later); got != id {
		t.Fatalf("Parked(the same call in a later turn) = %q, want %q", got, id)
	}
	if got := parked(call(1)); got != "" {
		t.Fatalf("the call's own key (a rerun of its turn) found %q", got)
	}
	other := later
	other.Args = json.RawMessage(`{"url":"https://example.com/y"}`)
	elsewhere := later
	elsewhere.Conv = "telegram:43"
	someoneElse := later
	someoneElse.Principal = "telegram:user:43"
	otherTool := later
	otherTool.Name = "web.search"
	for name, c := range map[string]tool.Call{"another url": other, "another conversation": elsewhere,
		"another person": someoneElse, "another tool": otherTool} {
		if got := parked(c); got != "" {
			t.Errorf("%s: Parked = %q", name, got)
		}
	}
	// Answered: the same call asks again.
	if _, err := f.s.Press(ctx, q.Buttons[1].ID, by("01PRESS")); err != nil {
		t.Fatal(err)
	}
	if got := parked(later); got != "" {
		t.Fatalf("an answered call is still found: %q", got)
	}
	// Past its expiry, even before the expirer marks it: asks again.
	id2, _ := f.ask(t, 2)
	if got := parked(later); got != id2 {
		t.Fatalf("Parked = %q, want %q", got, id2)
	}
	f.clk.add(pending.DefaultTTL)
	if got := parked(later); got != "" {
		t.Fatalf("an expired call is still found: %q", got)
	}
}

// Whether a call failed is stored as a flag, not read from its text: a
// rerun and a second press show how it really ended.
func TestFinishStoresWhetherTheCallFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	failed, _ := f.ask(t, 1)
	ok, _ := f.ask(t, 2)
	if err := f.s.Finish(ctx, failed, pending.Outcome{Result: "boom", Failed: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Finish(ctx, ok, pending.Outcome{Result: "error: none found"}); err != nil {
		t.Fatal(err)
	}
	if it, err := f.s.Get(ctx, failed); err != nil || !it.Failed {
		t.Fatalf("failed call %+v, %v", it, err)
	}
	if it, err := f.s.Get(ctx, ok); err != nil || it.Failed {
		t.Fatalf("a call whose text starts with error: %+v, %v", it, err)
	}
}

// The questions waiting for the owner are a gauge: an answered one leaves it.
func TestWaitingGauge(t *testing.T) {
	rec := telemetrytest.Install(t)
	f := newFixture(t)
	_, q := f.ask(t, 1)
	f.ask(t, 2)
	if got := rec.Value(t, "ganglion.confirmations.pending", nil); got != 2 {
		t.Fatalf("pending = %v, want 2", got)
	}
	if _, err := f.s.Press(context.Background(), q.Buttons[0].ID, by("ev1")); err != nil {
		t.Fatal(err)
	}
	if got := rec.Value(t, "ganglion.confirmations.pending", nil); got != 1 {
		t.Fatalf("pending after a press = %v, want 1", got)
	}
}
