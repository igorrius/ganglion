package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// fakeBuildJobs records what the build queue tells the job registry.
type fakeBuildJobs struct {
	mu       sync.Mutex
	started  []jobs.Job
	progress map[string]int
	ended    map[string]jobs.State
}

func newFakeBuildJobs() *fakeBuildJobs {
	return &fakeBuildJobs{progress: map[string]int{}, ended: map[string]jobs.State{}}
}

func (f *fakeBuildJobs) Start(_ context.Context, j jobs.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, j)
	return nil
}

func (f *fakeBuildJobs) Progress(key string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress[key] = n
}

func (f *fakeBuildJobs) End(_ context.Context, key string, s jobs.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended[key] = s
	return nil
}

// starts are the jobs started with key, in order.
func (f *fakeBuildJobs) starts(key string) []jobs.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []jobs.Job
	for _, j := range f.started {
		if j.Key == key {
			out = append(out, j)
		}
	}
	return out
}

func (f *fakeBuildJobs) endOf(key string) jobs.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended[key]
}

// puts collects the events the queue puts. The first fail puts fail; tried,
// when set, hears of every put.
type puts struct {
	mu    sync.Mutex
	evs   []event.Event
	fail  int
	tried chan error
}

func (p *puts) put(_ context.Context, e event.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	if p.fail > 0 {
		p.fail--
		err = errors.New("the inbox is down")
	} else {
		p.evs = append(p.evs, e)
	}
	if p.tried != nil {
		p.tried <- err
	}
	return err
}

func (p *puts) all() []event.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]event.Event(nil), p.evs...)
}

// newQueue is a build queue over dir with the fakes; its retell pass never
// comes.
func newQueue(dir string, j builtin.BuildJobs, p *puts) *builtin.BuildQueue {
	return newQueueAfter(dir, j, p, func(time.Duration) <-chan time.Time { return nil })
}

// newQueueAfter is newQueue whose passes come from after.
func newQueueAfter(dir string, j builtin.BuildJobs, p *puts, after func(time.Duration) <-chan time.Time) *builtin.BuildQueue {
	return builtin.NewBuildQueue(builtin.BuildQueueDeps{Dir: dir, Jobs: j, Put: p.put,
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) }, After: after, Log: slog.New(slog.DiscardHandler)})
}

// jobsDir is the build records' folder of e.
func (e *moduleEnv) jobsDir() string { return filepath.Join(filepath.Dir(e.m.Builds), "build-jobs") }

// startBuild calls module.build with args as a call of its own and returns
// its immediate answer.
func (e *moduleEnv) startBuild(t *testing.T, args string) (tool.Result, error) {
	t.Helper()
	e.calls++
	c := call(builtin.ModuleBuildTool, args)
	c.N = e.calls
	return e.tool(builtin.ModuleBuildTool).Run(context.Background(), c)
}

// runBuild runs the next waiting build and returns the outcome of key.
func (e *moduleEnv) runBuild(t *testing.T, key string) builtin.BuildOutcome {
	t.Helper()
	if !e.m.Queue.Step(context.Background()) {
		t.Fatal("no build was waiting")
	}
	o, err := e.m.Queue.Outcome(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// buildNow builds with args and returns the answer the build ended with,
// as module.build answered it before builds ran in the background.
func (e *moduleEnv) buildNow(t *testing.T, args string) tool.Result {
	t.Helper()
	res, err := e.startBuild(t, args)
	if err != nil {
		t.Fatal(err)
	}
	o := e.runBuild(t, res.Job)
	return tool.Result{Text: o.Answer, External: o.External}
}

// R-RUN-9, ADR-0069: module.build answers at once with the job's key and
// a card, queues the build, and its end stores the answer, ends the card and
// puts module.built into the call's conversation.
func TestModuleBuildAnswersAtOnceAndItsEndResumesTheConversation(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	art := artifact(demoManifest, 1024, 1)
	e.b.art = art
	res, err := e.startBuild(t, `{"name":"demo-reader","note":"first"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.External || res.Job == "" || !strings.HasPrefix(res.Job, "build:demo-reader:") {
		t.Fatalf("answer %+v", res)
	}
	var ans struct{ Building, Job, Next string }
	if err := json.Unmarshal([]byte(res.Text), &ans); err != nil || ans.Building != "demo-reader" || ans.Job != res.Job ||
		ans.Next != "the result comes back as a note when the build ends; a card in the chat shows its progress" {
		t.Fatalf("answer %s, %v", res.Text, err)
	}
	if len(e.b.built) != 0 {
		t.Fatal("the build ran inside the call")
	}
	st := e.jobs.starts(res.Job)
	if len(st) == 0 || st[0].Kind != jobs.KindBuild || st[0].Title != "demo-reader" || st[0].Conv != "telegram:42" ||
		st[0].Transport != "telegram" || st[0].Principal != "telegram:user:42" {
		t.Fatalf("card %+v", st)
	}
	o := e.runBuild(t, res.Job)
	if !strings.Contains(o.Answer, art.SHA256) || !o.External || o.State != jobs.StateDone || o.Conv != "telegram:42" ||
		o.Principal != "telegram:user:42" || o.Module != "demo-reader" {
		t.Fatalf("outcome %+v", o)
	}
	if e.jobs.endOf(res.Job) != jobs.StateDone {
		t.Fatalf("the card ended as %q", e.jobs.endOf(res.Job))
	}
	evs := e.puts.all()
	if len(evs) != 1 || evs[0].Kind != event.KindModuleBuilt || evs[0].Conv != "telegram:42" || evs[0].Source != "telegram" ||
		evs[0].IdemKey != res.Job+":built" {
		t.Fatalf("events %+v", evs)
	}
	var p event.ModuleBuilt
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p.Key != res.Job || p.Module != "demo-reader" {
		t.Fatalf("payload %s", evs[0].Payload)
	}
}

// A failed build ends its card as failed; the stored answer is the log's
// end, outside content, as module.build answered before.
func TestAFailedBuildEndsItsCardAsFailed(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.err, e.b.log = errors.New("builder: building demo-reader: exit status 101"), "error[E0425]: a hostile line"
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	o := e.runBuild(t, res.Job)
	if !o.External || !strings.Contains(o.Answer, "a hostile line") || o.State != jobs.StateFailed || e.jobs.endOf(res.Job) != jobs.StateFailed {
		t.Fatalf("outcome %+v", o)
	}
}

// The build tells the card how many crates it compiled.
func TestABuildsProgressReachesItsCard(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art, e.b.crates = artifact(demoManifest, 1024, 1), 14
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	e.runBuild(t, res.Job)
	if n := e.jobs.progress[res.Job]; n != 14 {
		t.Fatalf("progress %d", n)
	}
}

// The same call asked again (a turn run once more) queues no second build.
func TestTheSameCallQueuesOneBuild(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art = artifact(demoManifest, 1024, 1)
	c := call(builtin.ModuleBuildTool, `{"name":"demo-reader"}`)
	first, err := e.tool(builtin.ModuleBuildTool).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.tool(builtin.ModuleBuildTool).Run(context.Background(), c)
	if err != nil || again.Job != first.Job {
		t.Fatalf("again %+v, %v", again, err)
	}
	e.runBuild(t, first.Job)
	if e.m.Queue.Step(context.Background()) || len(e.b.built) != 1 {
		t.Fatalf("built %d times", len(e.b.built))
	}
}

// ADR-0069: while a module's build runs or waits, a second build of it and
// a write to it are refused in the node's words; other modules are not.
func TestAModuleBeingBuiltRefusesABuildAndAWrite(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.source(t, "other")
	e.b.art = artifact(demoManifest, 1024, 1)
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	const want = "demo-reader is being built; wait for its result"
	if _, err := e.startBuild(t, `{"name":"demo-reader"}`); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a second build while one waits: %v", err)
	}
	if _, err := e.write(t, "demo-reader", "src/lib.rs", "x"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a write while a build waits: %v", err)
	}
	if _, err := e.write(t, "other", "src/lib.rs", "x"); err != nil {
		t.Fatalf("another module's write: %v", err)
	}
	// While it runs.
	e.b.gate, e.b.running = make(chan struct{}), make(chan string, 1)
	done := make(chan struct{})
	go func() { e.m.Queue.Step(context.Background()); close(done) }()
	<-e.b.running
	if _, err := e.write(t, "demo-reader", "src/lib.rs", "x"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a write while the build runs: %v", err)
	}
	close(e.b.gate)
	<-done
	if _, err := e.write(t, "demo-reader", "src/lib.rs", "x"); err != nil {
		t.Fatalf("a write after the build ended: %v", err)
	}
	if _, err := e.m.Queue.Outcome(context.Background(), res.Job); err != nil {
		t.Fatal(err)
	}
}

// One build at a time: the second module's waits, its card says queued, and
// it runs once the first ended, its time counted from then.
func TestBuildsRunOneAfterTheOther(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.source(t, "other")
	e.b.art = artifact(demoManifest, 1024, 1)
	first, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.startBuild(t, `{"name":"other"}`)
	if err != nil {
		t.Fatal(err)
	}
	if st := e.jobs.starts(second.Job); len(st) != 1 || st[0].State != jobs.StateQueued {
		t.Fatalf("the second card %+v", st)
	}
	e.runBuild(t, first.Job)
	if len(e.b.built) != 1 || e.b.built[0].Name != "demo-reader" || e.jobs.endOf(second.Job) != "" {
		t.Fatalf("built %+v", e.b.built)
	}
	e.runBuild(t, second.Job)
	if len(e.b.built) != 2 || e.b.built[1].Name != "other" {
		t.Fatalf("built %+v", e.b.built)
	}
	if st := e.jobs.starts(second.Job); len(st) != 2 || st[1].State != jobs.StateRunning {
		t.Fatalf("the second card was not started as running: %+v", st)
	}
}

// Run works the queue until its context ends.
func TestTheQueueRunsWhatItIsGiven(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art = artifact(demoManifest, 1024, 1)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- e.m.Queue.Run(ctx) }()
	e.b.running = make(chan string, 1)
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	<-e.b.running
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Queue.Outcome(context.Background(), res.Job); err != nil {
		t.Fatal(err)
	}
}

// A restart during a build: the next start ends it as interrupted, in the
// node's words, and puts module.built so the agent is told; it is told once.
func TestARestartDuringABuildEndsItAsInterruptedAndTellsTheAgent(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	// The node stops here; a new process starts over the same folder.
	j, p := newFakeBuildJobs(), &puts{}
	q := newQueue(e.jobsDir(), j, p)
	if err := q.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, err := q.Outcome(context.Background(), res.Job)
	if err != nil || o.State != jobs.StateInterrupted || o.External || !strings.Contains(o.Answer, "interrupted") {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if evs := p.all(); len(evs) != 1 || evs[0].Kind != event.KindModuleBuilt || evs[0].Conv != "telegram:42" {
		t.Fatalf("events %+v", evs)
	}
	if q.Step(context.Background()) {
		t.Fatal("the interrupted build ran after the restart")
	}
	if err := newQueue(e.jobsDir(), j, p).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if evs := p.all(); len(evs) != 1 {
		t.Fatalf("told %d times", len(evs))
	}
	if _, err := q.Outcome(context.Background(), "build:nothing:0"); !errors.Is(err, builtin.ErrNoBuildJob) {
		t.Fatalf("unknown key: %v", err)
	}
}

// The key names the module and the call, so a second call builds again.
func TestEachCallIsABuildOfItsOwn(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art = artifact(demoManifest, 1024, 1)
	keys := map[string]bool{}
	for i := range 2 {
		res, err := e.startBuild(t, `{"name":"demo-reader"}`)
		if err != nil {
			t.Fatal(strconv.Itoa(i), err)
		}
		e.runBuild(t, res.Job)
		keys[res.Job] = true
	}
	if len(keys) != 2 || len(e.b.built) != 2 {
		t.Fatalf("keys %v, built %d", keys, len(e.b.built))
	}
}

// Owner's ruling (2026-11-10): at most MaxBuildsInARow builds of a module in
// a row in a conversation without a person's message there; the next is
// refused in the node's words, and a message from the owner starts the count
// afresh. Another module and another conversation count apart.
func TestBuildsInARowAreCappedUntilTheOwnerWrites(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.source(t, "other")
	e.b.err, e.b.log = errors.New("builder: building demo-reader: exit status 101"), "error[E0308]"
	for i := range builtin.MaxBuildsInARow {
		res, err := e.startBuild(t, `{"name":"demo-reader"}`)
		if err != nil {
			t.Fatalf("build %d: %v", i+1, err)
		}
		e.runBuild(t, res.Job)
	}
	const want = "demo-reader was built 5 times in a row; ask the owner before building again"
	if _, err := e.startBuild(t, `{"name":"demo-reader"}`); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the sixth build: %v", err)
	}
	if res, err := e.startBuild(t, `{"name":"other"}`); err != nil {
		t.Fatalf("another module: %v", err)
	} else {
		e.runBuild(t, res.Job)
	}
	e.m.Queue.OwnerSpoke("telegram:7")
	if _, err := e.startBuild(t, `{"name":"demo-reader"}`); err == nil {
		t.Fatal("a message in another conversation reset the count")
	}
	e.m.Queue.OwnerSpoke("telegram:42")
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatalf("after the owner wrote: %v", err)
	}
	e.runBuild(t, res.Job)
}

// A module.built whose put failed is put again by the queue's next pass, not
// only at the next start.
func TestAFailedAnnouncementIsPutAgainByTheNextPass(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art = artifact(demoManifest, 1024, 1)
	p := &puts{fail: 1, tried: make(chan error, 4)}
	tick := make(chan time.Time)
	e.m.Queue = newQueueAfter(e.jobsDir(), e.jobs, p, func(time.Duration) <-chan time.Time { return tick })
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- e.m.Queue.Run(ctx) }()
	if _, err := e.startBuild(t, `{"name":"demo-reader"}`); err != nil {
		t.Fatal(err)
	}
	if err := <-p.tried; err == nil {
		t.Fatal("the first put did not fail")
	}
	tick <- time.Time{}
	if err := <-p.tried; err != nil {
		t.Fatalf("the pass's put: %v", err)
	}
	tick <- time.Time{} // told: the next pass puts nothing
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if evs := p.all(); len(evs) != 1 || evs[0].Kind != event.KindModuleBuilt {
		t.Fatalf("events %+v", evs)
	}
}

// A build the stopping node cut short stays running and untold; the next
// start ends that running record as interrupted and tells the agent.
func TestABuildCutShortByShutdownIsToldAtTheNextStart(t *testing.T) {
	e := newModuleEnv(t)
	e.source(t, "demo-reader")
	e.b.art = artifact(demoManifest, 1024, 1)
	e.b.running, e.b.gate = make(chan string, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- e.m.Queue.Run(ctx) }()
	res, err := e.startBuild(t, `{"name":"demo-reader"}`)
	if err != nil {
		t.Fatal(err)
	}
	<-e.b.running
	cancel()
	close(e.b.gate)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if o, err := e.m.Queue.Outcome(context.Background(), res.Job); err != nil || o.State != jobs.StateRunning {
		t.Fatalf("after the stop: %+v, %v", o, err)
	}
	if len(e.puts.all()) != 0 || e.jobs.endOf(res.Job) != "" {
		t.Fatal("a build cut short was told or its card ended")
	}
	p := &puts{}
	q := newQueue(e.jobsDir(), newFakeBuildJobs(), p)
	if err := q.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o, _ := q.Outcome(context.Background(), res.Job); o.State != jobs.StateInterrupted || !strings.Contains(o.Answer, "interrupted") {
		t.Fatalf("after the start: %+v", o)
	}
	if evs := p.all(); len(evs) != 1 || evs[0].Kind != event.KindModuleBuilt {
		t.Fatalf("events %+v", evs)
	}
}
