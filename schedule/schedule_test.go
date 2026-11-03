package schedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
)

const owner = "telegram:user:42"

// t0 is a Tuesday morning in Kyiv.
var t0 = time.Date(2026, 11, 3, 8, 0, 30, 0, time.UTC)

// inbox is queue.Queue.Put in memory: an IdemKey already stored is a
// no-op. fail makes the next Put fail.
type inbox struct {
	mu     sync.Mutex
	events []event.Event
	keys   map[string]bool
	fail   error
}

func (q *inbox) Put(_ context.Context, e event.Event) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail != nil {
		err := q.fail
		q.fail = nil
		return err
	}
	if q.keys == nil {
		q.keys = map[string]bool{}
	}
	if !q.keys[e.IdemKey] {
		q.keys[e.IdemKey] = true
		q.events = append(q.events, e)
	}
	return nil
}

func (q *inbox) take() []event.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.events
	q.events = nil
	return out
}

type env struct {
	store *schedule.Store
	sched *schedule.Scheduler
	q     *inbox
	now   time.Time
	loc   *time.Location
}

func newEnv(t *testing.T, opts ...schedule.Option) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := schedule.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{store: st, q: &inbox{}, now: t0, loc: loc}
	e.sched = schedule.New(st, e.q, loc, func() time.Time { return e.now }, opts...)
	return e
}

func task(id, cron string) schedule.Job {
	return schedule.Job{ID: id, Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindTask, Cron: cron,
		Text: "send me the exchange rates", Grant: tool.Grant{Tools: []string{"web.fetch"}, Hosts: []string{"bank.gov.ua"}, ReplyTo: "telegram:42"},
		Created: t0}
}

func reminder(id string, at time.Time) schedule.Job {
	return schedule.Job{ID: id, Owner: owner, Conv: "telegram:42", Transport: "telegram", Kind: schedule.KindReminder, At: at,
		Text: "call mum", Created: t0}
}

func (e *env) create(t *testing.T, j schedule.Job) {
	t.Helper()
	if err := e.store.Create(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

func (e *env) tick(t *testing.T) []event.Event {
	t.Helper()
	if err := e.sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e.q.take()
}

func payload(t *testing.T, ev event.Event) event.Schedule {
	t.Helper()
	var p event.Schedule
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func cronKey(id string, slot time.Time) string {
	return "cron:" + id + ":" + strconv.FormatInt(slot.UnixMilli(), 10)
}

// After five hours down, an hourly job runs once, for the latest slot it
// missed, and then on its schedule again.
func TestCatchUpRunsOnce(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("j1", "0 * * * *"))
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("fired at creation: %+v", got)
	}
	e.now = t0.Add(5*time.Hour + 10*time.Minute) // 15:10 Kyiv; 11:00..15:00 missed
	got := e.tick(t)
	slot := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC) // 15:00 Kyiv
	if len(got) != 1 {
		t.Fatalf("%d events after downtime, want 1: %+v", len(got), got)
	}
	ev := got[0]
	if ev.IdemKey != cronKey("j1", slot) || ev.Kind != event.KindSchedule || ev.Conv != "task:j1" || ev.Source != schedule.Source ||
		payload(t, ev) != (event.Schedule{Job: "j1", Scheduled: slot.UnixMilli()}) || !ev.At.Equal(e.now) || ev.ID == "" {
		t.Fatalf("event %+v", ev)
	}
	e.now = e.now.Add(10 * time.Minute)
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("ran again in the same hour: %+v", got)
	}
	e.now = time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	if got := e.tick(t); len(got) != 1 || got[0].IdemKey != cronKey("j1", e.now) {
		t.Fatalf("the next hour: %+v", got)
	}
}

// A crash after the run was recorded but before its event was stored is
// made good on the next tick, with the same key.
func TestRunRecordedButNotPutIsPutAgain(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("j1", "0 * * * *"))
	e.now = t0.Add(time.Hour)
	e.q.fail = errors.New("disk full")
	if err := e.sched.Tick(context.Background()); err == nil {
		t.Fatal("a failed put was not reported")
	}
	e.now = e.now.Add(time.Minute)
	got := e.tick(t)
	if len(got) != 1 || got[0].IdemKey != cronKey("j1", time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("after the failed put: %+v", got)
	}
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("put twice: %+v", got)
	}
}

// A job whose event cannot be put does not hold back the others.
func TestOneFailingJobDoesNotHoldBackOthers(t *testing.T) {
	e := newEnv(t)
	e.create(t, task("j1", "0 * * * *"))
	e.create(t, task("j2", "0 * * * *"))
	e.now = t0.Add(time.Hour)
	e.q.fail = errors.New("disk full")
	if err := e.sched.Tick(context.Background()); err == nil {
		t.Fatal("the failure was not reported")
	}
	if got := e.q.take(); len(got) != 1 || got[0].Conv != "task:j2" {
		t.Fatalf("events %+v, want j2's", got)
	}
	if got := e.tick(t); len(got) != 1 || got[0].Conv != "task:j1" {
		t.Fatalf("the next tick: %+v, want j1's", got)
	}
}

func TestDeletedJobDoesNotFire(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.create(t, task("j1", "0 * * * *"))
	e.create(t, reminder("r1", t0.Add(time.Hour)))
	for _, id := range []string{"j1", "r1"} {
		if err := e.store.Delete(ctx, id, owner); err != nil {
			t.Fatal(err)
		}
	}
	e.now = t0.Add(3 * time.Hour)
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("a deleted job fired: %+v", got)
	}
	if err := e.sched.RunNow(ctx, "j1", owner); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("run now of a deleted job: %v", err)
	}
	j, err := e.store.Get(ctx, "j1")
	if err != nil || !j.Deleted {
		t.Fatalf("get %+v, %v", j, err)
	}
	if list, err := e.store.List(ctx, owner); err != nil || len(list) != 0 {
		t.Fatalf("list %+v, %v", list, err)
	}
	if err := e.store.Delete(ctx, "j1", owner); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestSystemJobCannotBeDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	audit := task("system:memory-audit", "0 4 * * *")
	audit.System = true
	if err := e.store.Ensure(ctx, audit); err != nil {
		t.Fatal(err)
	}
	changed := audit
	changed.Text = "something else"
	if err := e.store.Ensure(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Delete(ctx, audit.ID, owner); !errors.Is(err, schedule.ErrSystem) {
		t.Fatalf("delete: %v, want ErrSystem", err)
	}
	list, err := e.store.List(ctx, owner)
	if err != nil || len(list) != 1 || !list[0].System || list[0].Text != audit.Text || list[0].Deleted {
		t.Fatalf("list %+v, %v", list, err)
	}
}

func TestRunNowKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.create(t, task("j1", "0 9 * * *"))
	e.create(t, reminder("r1", t0.Add(time.Hour)))
	if err := e.sched.RunNow(ctx, "j1", owner); err != nil {
		t.Fatal(err)
	}
	got := e.q.take()
	want := "cron:j1:now:" + strconv.FormatInt(t0.UnixMilli(), 10)
	if len(got) != 1 || got[0].IdemKey != want || got[0].Conv != "task:j1" || got[0].Kind != event.KindSchedule ||
		payload(t, got[0]) != (event.Schedule{Job: "j1", Scheduled: t0.UnixMilli()}) {
		t.Fatalf("events %+v, want key %s", got, want)
	}
	// The same token is the same run; a caller that is rerun passes its
	// own stable token.
	if err := e.sched.RunKey(ctx, "j1", owner, strconv.FormatInt(t0.UnixMilli(), 10)); err != nil || len(e.q.take()) != 0 {
		t.Fatalf("run with the same token: %v", err)
	}
	for range 2 {
		if err := e.sched.RunKey(ctx, "j1", owner, "01CALL:3"); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.q.take(); len(got) != 1 || got[0].IdemKey != "cron:j1:now:01CALL:3" {
		t.Fatalf("run by token: %+v", got)
	}
	if err := e.sched.RunKey(ctx, "j1", owner, ""); !errors.Is(err, schedule.ErrInvalid) {
		t.Fatalf("no token: %v", err)
	}
	if err := e.sched.RunNow(ctx, "j1", "telegram:user:7"); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("someone else's job: %v", err)
	}
	if err := e.sched.RunNow(ctx, "r1", owner); !errors.Is(err, schedule.ErrNotTask) {
		t.Fatalf("a reminder: %v", err)
	}
	// A run now is not a scheduled run: the 09:00 slot still comes.
	if err := e.store.Delete(ctx, "r1", owner); err != nil {
		t.Fatal(err)
	}
	e.now = time.Date(2026, 11, 4, 7, 0, 0, 0, time.UTC)
	if got := e.tick(t); len(got) != 1 || got[0].IdemKey != cronKey("j1", e.now) {
		t.Fatalf("the slot after a run now: %+v", got)
	}
}

// A one-shot reminder fires once, into the conversation it was made in,
// and leaves the list.
func TestReminderFiresOnceInItsConversation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	e.create(t, reminder("r1", at))
	e.now = at.Add(-time.Second)
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("early: %+v", got)
	}
	e.now = at.Add(2 * time.Hour)
	got := e.tick(t)
	if len(got) != 1 || got[0].Conv != "telegram:42" || got[0].IdemKey != cronKey("r1", at) {
		t.Fatalf("events %+v", got)
	}
	e.now = e.now.Add(24 * time.Hour)
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("again: %+v", got)
	}
	if list, err := e.store.List(ctx, owner); err != nil || len(list) != 0 {
		t.Fatalf("a fired reminder is still listed: %+v, %v", list, err)
	}
}

// A daily job on the autumn day runs once, though 03:00 happens twice.
func TestTickAcrossFallBack(t *testing.T) {
	e := newEnv(t)
	j := task("j1", "0 3 * * *")
	j.Created = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	e.create(t, j)
	var keys []string
	for now := j.Created; now.Before(time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)); now = now.Add(time.Minute) {
		e.now = now
		for _, ev := range e.tick(t) {
			keys = append(keys, ev.IdemKey)
		}
	}
	if len(keys) != 1 || keys[0] != cronKey("j1", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("runs %v", keys)
	}
}

func TestCreateValidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(j *schedule.Job){
		"no id":              func(j *schedule.Job) { j.ID = "" },
		"no owner":           func(j *schedule.Job) { j.Owner = "" },
		"no conv":            func(j *schedule.Job) { j.Conv = "" },
		"no text":            func(j *schedule.Job) { j.Text = " " },
		"unknown kind":       func(j *schedule.Job) { j.Kind = "alarm" },
		"no time":            func(j *schedule.Job) { j.Cron = "" },
		"cron and at":        func(j *schedule.Job) { j.At = t0 },
		"bad cron":           func(j *schedule.Job) { j.Cron = "61 * * * *" },
		"no created":         func(j *schedule.Job) { j.Created = time.Time{} },
		"task without reply": func(j *schedule.Job) { j.Grant.ReplyTo = "" },
		"reminder with grant": func(j *schedule.Job) {
			j.Kind = schedule.KindReminder
		},
	} {
		j := task("j-"+name, "0 * * * *")
		mutate(&j)
		if err := e.store.Create(ctx, j); !errors.Is(err, schedule.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	e.create(t, task("j1", "0 * * * *"))
	if err := e.store.Create(ctx, task("j1", "0 * * * *")); !errors.Is(err, schedule.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.store.Get(ctx, "nope"); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
	if err := e.store.Delete(ctx, "j1", "telegram:user:7"); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("delete someone else's: %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := task("j1", "*/5 * * * *")
	e.create(t, j)
	r := reminder("r1", time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC))
	r.Created = t0.Add(time.Second)
	e.create(t, r)
	other := task("j2", "0 * * * *")
	other.Owner = "telegram:user:7"
	e.create(t, other)
	list, err := e.store.List(ctx, owner)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v, %v", list, err)
	}
	got := list[0]
	if got.ID != j.ID || got.Cron != j.Cron || got.Text != j.Text || got.Transport != "telegram" || got.Kind != schedule.KindTask ||
		len(got.Grant.Tools) != 1 || got.Grant.Hosts[0] != "bank.gov.ua" || got.Grant.ReplyTo != "telegram:42" || !got.Created.Equal(t0) {
		t.Fatalf("job %+v", got)
	}
	if list[1].ID != "r1" || !list[1].At.Equal(r.At) || list[1].Kind != schedule.KindReminder {
		t.Fatalf("reminder %+v", list[1])
	}
}

// Run ticks at once and then at each minute boundary of the clock, until
// its context ends.
func TestRunTicksEachMinute(t *testing.T) {
	waits := make(chan time.Duration, 4)
	fire := make(chan time.Time)
	e := newEnv(t, schedule.WithAfter(func(d time.Duration) <-chan time.Time {
		waits <- d
		return fire
	}))
	e.create(t, task("j1", "* * * * *"))
	e.now = t0.Add(time.Minute) // 08:01:30
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.sched.Run(ctx) }()
	if d := <-waits; d != 30*time.Second {
		t.Fatalf("waits %v, want the rest of the minute", d)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := e.q.take(); len(got) != 1 {
		t.Fatalf("the first tick put %+v", got)
	}
	if e.sched.Name() != "scheduler" {
		t.Fatalf("name %q", e.sched.Name())
	}
}

// Ruling R36: a system job runs code the node registered, without a
// model. The node owns its system jobs: EnsureSystem replaces a stored
// one, so a job stored by an older release (the audit as a model task)
// and an earlier first admin give way at the next start.
func TestEnsureSystemReplacesTheStoredJob(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := task("system:memory-audit", "0 4 * * *")
	old.System = true
	if err := e.store.Ensure(ctx, old); err != nil {
		t.Fatal(err)
	}
	sys := schedule.Job{ID: old.ID, Owner: "telegram:user:7", Conv: "telegram:7", Transport: "telegram", Kind: schedule.KindSystem,
		Cron: "0 4 * * *", Text: "Memory audit", System: true, Created: t0.Add(time.Hour)}
	if err := e.store.EnsureSystem(ctx, sys); err != nil {
		t.Fatal(err)
	}
	got, err := e.store.Get(ctx, old.ID)
	if err != nil || got.Kind != schedule.KindSystem || got.Owner != sys.Owner || got.Conv != sys.Conv || got.Text != sys.Text ||
		len(got.Grant.Tools) != 0 || !got.System || got.EventConv() != "telegram:7" {
		t.Fatalf("job %+v, %v", got, err)
	}
	if err := e.store.EnsureSystem(ctx, sys); err != nil {
		t.Fatalf("again: %v", err)
	}
	// Not a system job, or one with a grant, is refused; a person's job
	// is never replaced.
	mine := task("j1", "0 9 * * *")
	e.create(t, mine)
	for name, j := range map[string]schedule.Job{
		"not system":   {ID: "j1", Owner: owner, Conv: "telegram:42", Kind: schedule.KindSystem, Cron: "0 4 * * *", Text: "x", Created: t0},
		"with a grant": {ID: "s2", Owner: owner, Conv: "telegram:42", Kind: schedule.KindSystem, Cron: "0 4 * * *", Text: "x", System: true, Created: t0, Grant: tool.Grant{Tools: []string{"clock"}}},
		"a task":       {ID: "j1", Owner: owner, Conv: "telegram:42", Kind: schedule.KindTask, Cron: "0 4 * * *", Text: "x", System: true, Created: t0, Grant: tool.Grant{ReplyTo: "telegram:42"}},
	} {
		if err := e.store.EnsureSystem(ctx, j); !errors.Is(err, schedule.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	taken := sys
	taken.ID = "j1"
	if err := e.store.EnsureSystem(ctx, taken); !errors.Is(err, schedule.ErrExists) {
		t.Errorf("a system job over a person's: %v, want ErrExists", err)
	}
	if got, _ := e.store.Get(ctx, "j1"); got.Kind != schedule.KindTask || got.System {
		t.Fatalf("a person's job was replaced: %+v", got)
	}
	if err := e.store.Create(ctx, schedule.Job{ID: "s3", Owner: owner, Conv: "telegram:42", Kind: schedule.KindSystem,
		Cron: "0 4 * * *", Text: "x", Created: t0}); !errors.Is(err, schedule.ErrInvalid) {
		t.Fatalf("a system kind without System: %v", err)
	}
}
