package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// outbox records the messages the registry stores. With err set it refuses
// them; with hold set each Enqueue says so on entered and waits for hold.
type outbox struct {
	mu      sync.Mutex
	msgs    []event.Outgoing
	err     error
	hold    chan struct{}
	entered chan struct{}
}

func (o *outbox) Enqueue(_ context.Context, msgs ...event.Outgoing) error {
	o.mu.Lock()
	hold, entered := o.hold, o.entered
	o.mu.Unlock()
	if hold != nil {
		entered <- struct{}{}
		<-hold
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.msgs = append(o.msgs, msgs...)
	return nil
}

// fail makes the outbox refuse messages, or take them again with nil.
func (o *outbox) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

func (o *outbox) got() []event.Outgoing {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.msgs)
}

// clock is a time the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ticker is the registry's after func: each wait is reported on asked, and
// ends when the test fires it, never by sleeping.
type ticker struct {
	asked chan time.Duration
	fire  chan time.Time
}

func (k ticker) after(d time.Duration) <-chan time.Time {
	k.asked <- d
	return k.fire
}

// env is a registry over a fresh database.
type env struct {
	db    *sql.DB
	store *jobs.Store
	box   *outbox
	clock *clock
	tick  ticker
	lang  texts.Lang
	r     *jobs.Registry
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := sqlitedb.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := jobs.NewStore(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, store: store, clock: &clock{t: t0}, lang: texts.English}
	e.r = e.registry()
	return e
}

// registry is a new registry over e's store, as a restarted node makes.
func (e *env) registry() *jobs.Registry {
	e.box = &outbox{}
	e.tick = ticker{asked: make(chan time.Duration), fire: make(chan time.Time)}
	return jobs.New(jobs.Deps{Store: e.store, Outbox: e.box, Texts: texts.Shipped(),
		Lang: func(context.Context, string) (texts.Lang, error) { return e.lang, nil },
		Now:  e.clock.now, After: e.tick.after})
}

// run runs the registry until the test ends; it returns once Run waits for
// its first tick, so the restart's work is done.
func (e *env) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	e.waitTick(t)
}

// waitTick waits until Run asks for its next tick, and checks it is 10 s.
func (e *env) waitTick(t *testing.T) {
	t.Helper()
	select {
	case d := <-e.tick.asked:
		if d != 10*time.Second {
			t.Fatalf("the card ticks every %v, want 10s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not wait for a tick")
	}
}

// fire moves the clock by d and runs one tick to its end.
func (e *env) fire(t *testing.T, d time.Duration) {
	t.Helper()
	e.clock.add(d)
	e.tick.fire <- e.clock.now()
	e.waitTick(t)
}

func build(key, conv, module string) jobs.Job {
	return jobs.Job{Key: key, Kind: jobs.KindBuild, Conv: conv, Transport: "telegram", Principal: "telegram:user:1", Title: module}
}

func command(key, conv, argv string) jobs.Job {
	return jobs.Job{Key: key, Kind: jobs.KindCommand, Conv: conv, Transport: "telegram", Principal: "telegram:user:1", Title: argv}
}

// texts are the texts of msgs, in order.
func textsOf(msgs []event.Outgoing) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return out
}

func TestAStartedJobSendsOneCard(t *testing.T) {
	e := newEnv(t)
	j := command("t1:1", "telegram:1", "du -sh /home")
	for range 2 {
		if err := e.r.Start(t.Context(), j); err != nil {
			t.Fatal(err)
		}
	}
	got := e.box.got()
	if len(got) != 1 {
		t.Fatalf("messages %+v, want one card", got)
	}
	m := got[0]
	if m.IdemKey != "job:t1:1:card" || m.EditOf != "" || m.Conv != "telegram:1" || m.Transport != "telegram" ||
		m.Text != "⚙️ du -sh /home · command · 0:00" {
		t.Fatalf("card %+v", m)
	}
}

func TestTheTickerEditsTheCardEvery10Seconds(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	if err := e.r.Start(t.Context(), build("build:battery-alerts:1", "telegram:1", "battery-alerts")); err != nil {
		t.Fatal(err)
	}
	e.fire(t, 10*time.Second)
	e.r.Progress("build:battery-alerts:1", 14)
	e.fire(t, 35*time.Second)
	got := e.box.got()
	want := []string{"🔨 battery-alerts · build · 0:00", "🔨 battery-alerts · build · 0:10",
		"🔨 battery-alerts · build · 0:45 · crates built: 14"}
	if !slices.Equal(textsOf(got), want) {
		t.Fatalf("texts %q, want %q", textsOf(got), want)
	}
	for i, m := range got[1:] {
		if m.IdemKey != "job:build:battery-alerts:1:card:v"+string(rune('1'+i)) || m.EditOf != "job:build:battery-alerts:1:card" {
			t.Fatalf("edit %d: %+v", i+1, m)
		}
	}
}

func TestEndEditsTheCardToItsLastTextAndStopsTheEdits(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	ctx := t.Context()
	if err := e.r.Start(ctx, build("b:1", "telegram:1", "battery-alerts")); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Start(ctx, command("c:1", "telegram:1", "make")); err != nil {
		t.Fatal(err)
	}
	e.clock.add(40 * time.Second)
	if err := e.r.End(ctx, "c:1", jobs.StateFailed); err != nil {
		t.Fatal(err)
	}
	e.clock.add(52 * time.Second)
	if err := e.r.End(ctx, "b:1", jobs.StateDone); err != nil {
		t.Fatal(err)
	}
	if err := e.r.End(ctx, "b:1", jobs.StateDone); err != nil {
		t.Fatal(err)
	}
	e.fire(t, 10*time.Second)
	got := e.box.got()
	want := []string{"🔨 battery-alerts · build · 0:00", "⚙️ make · command · 0:00",
		"❌ make · command failed in 0:40", "✅ battery-alerts · build done in 1:32"}
	if !slices.Equal(textsOf(got), want) {
		t.Fatalf("texts %q, want %q", textsOf(got), want)
	}
	if got[3].IdemKey != "job:b:1:card:v1" || got[3].EditOf != "job:b:1:card" {
		t.Fatalf("final edit %+v", got[3])
	}
	if len(e.r.All()) != 0 {
		t.Fatalf("ended jobs still listed: %+v", e.r.All())
	}
}

func TestARestartEndsARunningJobAsInterrupted(t *testing.T) {
	e := newEnv(t)
	if err := e.r.Start(t.Context(), build("b:1", "telegram:1", "battery-alerts")); err != nil {
		t.Fatal(err)
	}
	e.r = e.registry() // the node restarted
	e.run(t)
	got := e.box.got()
	if len(got) != 1 || got[0].Text != "⚠️ battery-alerts · build interrupted by a restart" ||
		got[0].IdemKey != "job:b:1:card:v1" || got[0].EditOf != "job:b:1:card" || got[0].Conv != "telegram:1" {
		t.Fatalf("messages %+v", got)
	}
	if len(e.r.Running("telegram:1")) != 0 || len(e.r.All()) != 0 {
		t.Fatalf("an interrupted job is listed: %+v", e.r.All())
	}
	if err := e.r.Start(t.Context(), build("b:1", "telegram:1", "battery-alerts")); err != nil {
		t.Fatal(err)
	}
	if len(e.box.got()) != 1 {
		t.Fatalf("a second card for one key: %+v", e.box.got())
	}
}

func TestRunningListsOnlyTheConversationsOpenJobs(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	queued := build("b:2", "telegram:1", "solar")
	queued.State = jobs.StateQueued
	for _, j := range []jobs.Job{build("b:1", "telegram:1", "battery-alerts"), command("c:1", "telegram:2", "ls"),
		command("c:2", "telegram:1", "make"), queued} {
		e.clock.add(time.Second)
		if err := e.r.Start(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.r.End(ctx, "c:2", jobs.StateDone); err != nil {
		t.Fatal(err)
	}
	keys := func(js []jobs.Job) []string {
		out := []string{}
		for _, j := range js {
			out = append(out, j.Key)
		}
		return out
	}
	if got := keys(e.r.Running("telegram:1")); !slices.Equal(got, []string{"b:1", "b:2"}) {
		t.Fatalf("Running = %v", got)
	}
	if got := keys(e.r.All()); !slices.Equal(got, []string{"b:1", "c:1", "b:2"}) {
		t.Fatalf("All = %v", got)
	}
}

func TestAQueuedCardWaitsUntilItsJobRuns(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	j := build("b:1", "telegram:1", "solar")
	j.State = jobs.StateQueued
	if err := e.r.Start(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	e.fire(t, 10*time.Second)
	j.State = jobs.StateRunning
	if err := e.r.Start(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	e.fire(t, 10*time.Second)
	want := []string{"⏳ solar · build · queued", "🔨 solar · build · 0:00", "🔨 solar · build · 0:10"}
	if got := textsOf(e.box.got()); !slices.Equal(got, want) {
		t.Fatalf("texts %q, want %q", got, want)
	}
}

func TestTheCardIsInThePersonsLanguage(t *testing.T) {
	e := newEnv(t)
	e.lang = "ru"
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "du -sh /home")); err != nil {
		t.Fatal(err)
	}
	if got := e.box.got(); len(got) != 1 || got[0].Text != "⚙️ du -sh /home · команда · 0:00" {
		t.Fatalf("messages %+v", got)
	}
}

func TestATitleKeepsItsMarksAsWritten(t *testing.T) {
	e := newEnv(t)
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "rm *_old [x]")); err != nil {
		t.Fatal(err)
	}
	if got := e.box.got(); len(got) != 1 || got[0].Text != `⚙️ rm \*\_old \[x\] · command · 0:00` {
		t.Fatalf("messages %+v", got)
	}
}

func TestAJobNeedsAKeyAKindAndAConversation(t *testing.T) {
	e := newEnv(t)
	for _, j := range []jobs.Job{{Kind: jobs.KindBuild, Conv: "c", Transport: "t"}, {Key: "k", Conv: "c", Transport: "t"},
		{Key: "k", Kind: jobs.KindBuild, Transport: "t"}, {Key: "k", Kind: jobs.KindBuild, Conv: "c", Transport: "t", State: jobs.StateDone}} {
		if err := e.r.Start(t.Context(), j); err == nil {
			t.Errorf("Start(%+v) passed", j)
		}
	}
	if err := e.r.End(t.Context(), "k", jobs.StateRunning); err == nil {
		t.Error("End with an open state passed")
	}
	if len(e.box.got()) != 0 {
		t.Fatalf("messages %+v", e.box.got())
	}
}

func TestStatusListsTheOpenJobs(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	queued := build("b:2", "telegram:1", "solar")
	queued.State = jobs.StateQueued
	for _, j := range []jobs.Job{build("b:1", "telegram:1", "battery-alerts"), command("c:1", "telegram:2", "du -sh /home"), queued} {
		if err := e.r.Start(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.add(45 * time.Second) // all started at once: listed by key
	checks, err := e.r.Status(ctx, status.Scope{Now: e.clock.now()})
	if err != nil || len(checks) != 3 {
		t.Fatalf("checks %+v, %v", checks, err)
	}
	want := []struct {
		line  texts.Key
		title string
		span  bool
	}{{texts.StatusJobBuild, "battery-alerts", true}, {texts.StatusJobBuildQueued, "solar", false}, {texts.StatusJobCommand, "du -sh /home", true}}
	for i, c := range checks {
		w := want[i]
		if c.Section != status.SectionJobs || c.State != status.OK || c.Line != w.line || c.Vars["title"] != w.title {
			t.Errorf("check %d: %+v", i, c)
		}
		if d, ok := c.Spans["elapsed"]; ok != w.span || (ok && d != 45*time.Second) {
			t.Errorf("check %d: spans %v", i, c.Spans)
		}
	}
	if e.r.Name() != "jobs" {
		t.Errorf("name %q", e.r.Name())
	}
}

func TestTheNoteListsWhatRuns(t *testing.T) {
	now := t0.Add(2 * time.Minute)
	queued := build("b:2", "telegram:1", "solar")
	queued.State, queued.Started = jobs.StateQueued, t0
	b := build("b:1", "telegram:1", "battery-alerts")
	b.State, b.Started = jobs.StateRunning, now.Add(-45*time.Second)
	c := command("c:1", "telegram:1", "du -sh /home")
	c.State, c.Started = jobs.StateRunning, now.Add(-70*time.Second)
	if got, want := jobs.Note([]jobs.Job{b, c}, now), "[jobs] running: build battery-alerts 0:45; command du -sh /home 1:10"; got != want {
		t.Errorf("note %q, want %q", got, want)
	}
	if got, want := jobs.Note([]jobs.Job{b, queued}, now), "[jobs] running: build battery-alerts 0:45; queued: build solar"; got != want {
		t.Errorf("note %q, want %q", got, want)
	}
	if got := jobs.Note(nil, now); got != "" {
		t.Errorf("note of none %q", got)
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0:00", 45 * time.Second: "0:45", 92 * time.Second: "1:32",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03", -time.Second: "0:00", 1500 * time.Millisecond: "0:01"} {
		if got := jobs.Elapsed(d); got != want {
			t.Errorf("Elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

// A scheduled task's conversation is no chat: its job is kept and listed,
// but it gets no card and no edit (fix round 1).
func TestATasksJobHasNoCard(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	if err := e.r.Start(t.Context(), command("c:1", "task:01TASK", "du -sh /home")); err != nil {
		t.Fatal(err)
	}
	e.fire(t, 10*time.Second)
	if got := e.r.Running("task:01TASK"); len(got) != 1 || got[0].Key != "c:1" {
		t.Fatalf("Running = %+v", got)
	}
	if err := e.r.End(t.Context(), "c:1", jobs.StateDone); err != nil {
		t.Fatal(err)
	}
	if got := e.box.got(); len(got) != 0 {
		t.Fatalf("a task's job sent %+v", got)
	}
	if len(e.r.All()) != 0 {
		t.Fatalf("listed after its end: %+v", e.r.All())
	}
}

// A row that cannot be stored sends no card: the row comes first.
func TestARowThatIsNotStoredSendsNoCard(t *testing.T) {
	e := newEnv(t)
	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "ls")); err == nil {
		t.Fatal("Start passed with no database")
	}
	if got := e.box.got(); len(got) != 0 || len(e.r.All()) != 0 {
		t.Fatalf("messages %+v, jobs %+v", got, e.r.All())
	}
}

// A card the outbox refused is sent at the next tick, as the first message.
func TestARefusedCardIsSentAtTheNextTick(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	e.box.fail(errors.New("outbox: full"))
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "ls")); err == nil {
		t.Fatal("Start hid the refused card")
	}
	if len(e.r.All()) != 1 {
		t.Fatalf("the job was not kept: %+v", e.r.All())
	}
	e.box.fail(nil)
	e.fire(t, 10*time.Second)
	got := e.box.got()
	if len(got) != 1 || got[0].IdemKey != "job:c:1:card" || got[0].EditOf != "" || got[0].Text != "⚙️ ls · command · 0:10" {
		t.Fatalf("messages %+v", got)
	}
}

// An end whose last edit the outbox refused still ends the job.
func TestAnEndWithARefusedEditStillEndsTheJob(t *testing.T) {
	e := newEnv(t)
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "ls")); err != nil {
		t.Fatal(err)
	}
	e.box.fail(errors.New("outbox: full"))
	if err := e.r.End(t.Context(), "c:1", jobs.StateDone); err == nil {
		t.Fatal("End hid the refused edit")
	}
	if len(e.r.All()) != 0 || len(e.r.Running("telegram:1")) != 0 {
		t.Fatalf("an ended job is listed: %+v", e.r.All())
	}
	e.r = e.registry() // a restart finds it ended: nothing to interrupt
	e.run(t)
	if got := e.box.got(); len(got) != 0 {
		t.Fatalf("a restart edited an ended job's card: %+v", got)
	}
}

// A tick's sending does not hold the list: a turn reads Running meanwhile.
func TestATickSendsOutsideTheLock(t *testing.T) {
	e := newEnv(t)
	e.run(t)
	if err := e.r.Start(t.Context(), command("c:1", "telegram:1", "ls")); err != nil {
		t.Fatal(err)
	}
	hold, entered := make(chan struct{}), make(chan struct{})
	e.box.mu.Lock()
	e.box.hold, e.box.entered = hold, entered
	e.box.mu.Unlock()
	e.clock.add(10 * time.Second)
	e.tick.fire <- e.clock.now()
	<-entered // the tick waits inside Enqueue
	if got := e.r.Running("telegram:1"); len(got) != 1 {
		t.Fatalf("Running = %+v", got)
	}
	e.r.Progress("c:1", 3)
	close(hold)
	e.waitTick(t)
	if got := textsOf(e.box.got()); !slices.Equal(got, []string{"⚙️ ls · command · 0:00", "⚙️ ls · command · 0:10"}) {
		t.Fatalf("texts %q", got)
	}
	if got := e.r.Running("telegram:1"); got[0].Count != 3 {
		t.Fatalf("the count set during the tick was lost: %+v", got)
	}
}

func TestTheNoteCutsALongTitle(t *testing.T) {
	c := command("c:1", "telegram:1", strings.Repeat("x", 300))
	c.State, c.Started = jobs.StateRunning, t0
	want := "[jobs] running: command " + strings.Repeat("x", 200) + "… 0:00"
	if got := jobs.Note([]jobs.Job{c}, t0); got != want {
		t.Errorf("note %q", got)
	}
}
