package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/jobs"
)

// BuildJobs shows a module build to the person (jobs.Registry): its card
// from the call that asked for it, the crates it compiled, its end.
type BuildJobs interface {
	Start(ctx context.Context, j jobs.Job) error
	Progress(key string, count int)
	End(ctx context.Context, key string, s jobs.State) error
}

// NoBuildJobs shows no build.
type NoBuildJobs struct{}

// Start implements BuildJobs.
func (NoBuildJobs) Start(context.Context, jobs.Job) error { return nil }

// Progress implements BuildJobs.
func (NoBuildJobs) Progress(string, int) {}

// End implements BuildJobs.
func (NoBuildJobs) End(context.Context, string, jobs.State) error { return nil }

// PutEventFunc stores an event in the inbox; a stored IdemKey is a no-op
// (sqlqueue.Queue.Put).
type PutEventFunc func(ctx context.Context, e event.Event) error

// ErrNoBuildJob is a key the build queue keeps no record of.
var ErrNoBuildJob = errors.New("builtin: no record of this build")

// interruptedAnswer is what the agent reads of a build the node stopped by
// restarting: the node's words, not outside content.
const interruptedAnswer = `{"error":"interrupted: the node restarted while this build ran or waited, so it did not finish; build it again"}`

// keepBuildRecords is how long the record of a build that was told stays.
const keepBuildRecords = 7 * 24 * time.Hour

// BuildOutcome is a build as its record stands: who asked for it and, once
// it ended, the answer module.build would have given (the same JSON) and
// whether that answer is outside content (R-RUN-17).
type BuildOutcome struct {
	Key       string
	Module    string // the module's short name
	Conv      string
	Transport string
	Principal string
	Depth     int // the depth of the call that asked for it
	State     jobs.State
	Answer    string
	External  bool
}

// jobRecord is the stored form of a BuildOutcome. Told is set once its
// module.built event is in the inbox.
type jobRecord struct {
	Key       string     `json:"key"`
	Module    string     `json:"module"`
	Conv      string     `json:"conv"`
	Transport string     `json:"transport"`
	Principal string     `json:"principal"`
	Depth     int        `json:"depth"`
	State     jobs.State `json:"state"`
	Answer    string     `json:"answer,omitempty"`
	External  bool       `json:"external,omitempty"`
	Told      bool       `json:"told,omitempty"`
	Started   int64      `json:"started_ms"`
	Ended     int64      `json:"ended_ms,omitempty"`
}

// open reports whether the build has not ended.
func (r jobRecord) open() bool { return r.State == jobs.StateQueued || r.State == jobs.StateRunning }

// outcome is r as the agent reads it.
func (r jobRecord) outcome() BuildOutcome {
	return BuildOutcome{Key: r.Key, Module: r.Module, Conv: r.Conv, Transport: r.Transport, Principal: r.Principal, Depth: r.Depth,
		State: r.State, Answer: r.Answer, External: r.External}
}

// card is r's job for the registry, in state s.
func (r jobRecord) card(s jobs.State) jobs.Job {
	return jobs.Job{Key: r.Key, Kind: jobs.KindBuild, Conv: r.Conv, Transport: r.Transport, Principal: r.Principal, Title: r.Module,
		State: s}
}

// buildEnd is how a build ended: its answer and the card's last state.
type buildEnd struct {
	Answer   string
	External bool
	State    jobs.State
}

// buildWork runs one build, telling progress the crates it compiled.
type buildWork func(ctx context.Context, progress builder.ProgressFunc) buildEnd

// buildTask is a build waiting for its turn.
type buildTask struct {
	rec  jobRecord
	work buildWork
}

// BuildQueueDeps are what a build queue needs. Every field is required.
type BuildQueueDeps struct {
	// Dir keeps one record per build: <data>/modules/build-jobs, beside
	// the builds and the provenance records, out of the command sandbox's
	// reach.
	Dir  string
	Jobs BuildJobs
	Put  PutEventFunc
	Now  func() time.Time
	// After is the timer between two passes that tell the ended builds
	// whose module.built was not put (time.After).
	After func(time.Duration) <-chan time.Time
	Log   *slog.Logger
}

// RetellEvery is how often the queue tries again to put the module.built
// of an ended build whose put failed, as the host runner's pass does.
const RetellEvery = 30 * time.Second

// MaxBuildsInARow is how many builds of one module a conversation may ask
// for in a row without a message from a person (owner, 2026-11-10): a
// build's end resumes the agent at the build call's own depth, so this cap,
// not the depth limit, ends a write → build → fix loop that does not
// converge.
const MaxBuildsInARow = 5

// streakKey is a module in a conversation.
type streakKey struct{ conv, module string }

// BuildQueue runs module builds in the background, one at a time in the
// order they were asked for (R-RUN-9, ADR-0069, ruling 16 of M4c): a build
// takes minutes, longer than a model call may wait. Each build keeps a
// record with its answer, so the module.built turn reads it and a restart
// tells the agent the build was interrupted. It is a node component.
type BuildQueue struct {
	d    BuildQueueDeps
	wake chan struct{}

	mu      sync.Mutex
	waiting []*buildTask
	busy    map[string]string // module → the key of its build, running or waiting
	// streak counts the builds asked for in a row per conversation and
	// module since a person's last message there; in memory, so a restart
	// starts it afresh.
	streak map[streakKey]int
}

// NewBuildQueue returns a build queue over d.
func NewBuildQueue(d BuildQueueDeps) *BuildQueue {
	return &BuildQueue{d: d, wake: make(chan struct{}, 1), busy: map[string]string{}, streak: map[streakKey]int{}}
}

// Name implements node.Component.
func (*BuildQueue) Name() string { return "builds" }

// Run tells the agent of the builds a stopped node left, then runs the
// builds it is given, one at a time, until ctx ends, and every RetellEvery
// puts again the module.built of an ended build whose put failed. A build
// runs under ctx, not under the call that asked for it, and within the
// builder's own time limit; when the node stops, the build stops with it
// and the next start tells it as interrupted.
func (q *BuildQueue) Run(ctx context.Context) error {
	if err := q.recover(ctx); err != nil {
		q.logErr(ctx, "builds: the builds a stopped node left were not told", err)
	}
	for {
		for q.next(ctx) {
			if ctx.Err() != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-q.wake:
		case <-q.d.After(RetellEvery):
			q.retell(ctx)
		}
	}
}

// retell puts the module.built of every ended build not yet told.
func (q *BuildQueue) retell(ctx context.Context) {
	recs, err := q.list()
	if err != nil {
		q.logErr(ctx, "builds: the ended builds were not read", err)
		return
	}
	for _, rec := range recs {
		if !rec.open() && !rec.Told {
			q.tell(ctx, rec)
		}
	}
}

// OwnerSpoke starts the count of builds in a row afresh for every module of
// conv: a person wrote there (MaxBuildsInARow).
func (q *BuildQueue) OwnerSpoke(conv string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for k := range q.streak {
		if k.conv == conv {
			delete(q.streak, k)
		}
	}
}

// errTooManyBuilds refuses a build past MaxBuildsInARow.
func errTooManyBuilds(name string) error {
	return fmt.Errorf("%s was built %d times in a row; ask the owner before building again", name, MaxBuildsInARow)
}

// errBeingBuilt refuses work on a module whose build runs or waits.
func errBeingBuilt(name string) error {
	return fmt.Errorf("%s is being built; wait for its result", name)
}

// Building reports whether module name's build runs or waits.
func (q *BuildQueue) Building(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.busy[name]
	return ok
}

// submit queues rec's build and starts its card: running when no other build
// runs or waits, else queued. A module whose build runs or waits is refused,
// and so is one built MaxBuildsInARow times in a row in rec's conversation;
// a key that has a record already (the same call asked again) queues
// nothing. The module is reserved under the lock and the record written
// outside it; a write that fails gives the reservation back.
func (q *BuildQueue) submit(ctx context.Context, rec jobRecord, work buildWork) error {
	if _, err := q.load(rec.Key); err == nil {
		return nil
	} else if !errors.Is(err, ErrNoBuildJob) {
		return err
	}
	state, err := q.reserve(rec)
	if err != nil || state == "" {
		return err
	}
	rec.State, rec.Started = jobs.StateQueued, q.d.Now().UnixMilli()
	if err := q.save(rec); err != nil {
		q.release(rec)
		return err
	}
	q.mu.Lock()
	q.waiting = append(q.waiting, &buildTask{rec: rec, work: work})
	q.mu.Unlock()
	if err := q.d.Jobs.Start(ctx, rec.card(state)); err != nil {
		// The card is a sign of life, not the build: its end still comes.
		q.logErr(ctx, "builds: a build's card failed to start or waits for the next tick", err, "key", rec.Key)
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

// reserve takes rec's module for its build and counts it in the streak;
// state is the card's first state, "" when the key is reserved already.
func (q *BuildQueue) reserve(rec jobRecord) (jobs.State, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if key, ok := q.busy[rec.Module]; ok {
		if key == rec.Key {
			return "", nil
		}
		return "", errBeingBuilt(rec.Module)
	}
	sk := streakKey{rec.Conv, rec.Module}
	if q.streak[sk] >= MaxBuildsInARow {
		return "", errTooManyBuilds(rec.Module)
	}
	state := jobs.StateQueued
	if len(q.waiting) == 0 && len(q.busy) == 0 {
		state = jobs.StateRunning
	}
	q.busy[rec.Module] = rec.Key
	q.streak[sk]++
	return state, nil
}

// release gives back the reservation of rec's build that was not queued.
func (q *BuildQueue) release(rec jobRecord) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy[rec.Module] == rec.Key {
		delete(q.busy, rec.Module)
	}
	if sk := (streakKey{rec.Conv, rec.Module}); q.streak[sk] > 0 {
		q.streak[sk]--
	}
}

// next runs the build first in line and reports whether there was one.
func (q *BuildQueue) next(ctx context.Context) bool {
	q.mu.Lock()
	if len(q.waiting) == 0 {
		q.mu.Unlock()
		return false
	}
	task := q.waiting[0]
	q.waiting = q.waiting[1:]
	q.mu.Unlock()
	q.run(ctx, task)
	return true
}

// run runs one build and finishes it. A build the stopping node cut short
// is left open: the next start tells it as interrupted.
func (q *BuildQueue) run(ctx context.Context, task *buildTask) {
	rec := task.rec
	rec.State, rec.Started = jobs.StateRunning, q.d.Now().UnixMilli()
	if err := q.save(rec); err != nil {
		q.logErr(ctx, "builds: a build's start was not recorded", err, "key", rec.Key)
	}
	// The card counts its time from now; a card started as running stays.
	if err := q.d.Jobs.Start(ctx, rec.card(jobs.StateRunning)); err != nil {
		q.logErr(ctx, "builds: a build's card failed to start or waits for the next tick", err, "key", rec.Key)
	}
	end := task.work(ctx, func(n int) { q.d.Jobs.Progress(rec.Key, n) })
	if ctx.Err() != nil {
		return
	}
	q.finish(context.WithoutCancel(ctx), rec, end)
}

// finish stores how the build ended, frees its module, ends its card and
// tells the agent. A record that cannot be stored tells nothing: the agent
// could not read the answer.
func (q *BuildQueue) finish(ctx context.Context, rec jobRecord, end buildEnd) {
	rec.State, rec.Answer, rec.External, rec.Ended = end.State, end.Answer, end.External, q.d.Now().UnixMilli()
	saveErr := q.save(rec)
	q.mu.Lock()
	delete(q.busy, rec.Module)
	q.mu.Unlock()
	if err := q.d.Jobs.End(ctx, rec.Key, end.State); err != nil {
		q.logErr(ctx, "builds: a build's card was not ended", err, "key", rec.Key)
	}
	if saveErr != nil {
		q.logErr(ctx, "builds: a build's end was not recorded, so the agent is not told", saveErr, "key", rec.Key)
		return
	}
	q.tell(ctx, rec)
	q.prune()
}

// tell puts rec's module.built event and marks it told. A failed put is
// tried again at the next pass (retell) or start.
func (q *BuildQueue) tell(ctx context.Context, rec jobRecord) {
	if err := q.d.Put(ctx, builtEvent(rec, q.d.Now())); err != nil {
		q.logErr(ctx, "builds: the end of a build was not announced", err, "key", rec.Key)
		return
	}
	rec.Told = true
	if err := q.save(rec); err != nil {
		// The event is stored once by its key, so the next start's put is a no-op.
		q.logErr(ctx, "builds: a build's announcement was not recorded", err, "key", rec.Key)
	}
}

// builtEvent resumes the conversation of a build that ended. Its Source is
// the conversation's transport, so the answer goes back there; its key is
// fixed per build, so it is stored once.
func builtEvent(rec jobRecord, now time.Time) event.Event {
	payload, err := json.Marshal(event.ModuleBuilt{Key: rec.Key, Module: rec.Module})
	if err != nil {
		panic("builtin: a struct of strings always marshals: " + err.Error())
	}
	return event.Event{ID: event.NewID(now), IdemKey: rec.Key + ":built", Source: rec.Transport, Conv: rec.Conv,
		Kind: event.KindModuleBuilt, Payload: payload, At: now}
}

// recover ends as interrupted the builds a stopped node left open, and tells
// the agent of every ended build it was not told of. The registry ends their
// cards (jobs.Registry.Run).
func (q *BuildQueue) recover(ctx context.Context) error {
	recs, err := q.list()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		switch {
		case rec.open() && !q.mine(rec.Key):
			rec.State, rec.Answer, rec.External, rec.Ended = jobs.StateInterrupted, interruptedAnswer, false, q.d.Now().UnixMilli()
			if err := q.save(rec); err != nil {
				q.logErr(ctx, "builds: an interrupted build was not recorded", err, "key", rec.Key)
				continue
			}
			q.tell(ctx, rec)
		case !rec.open() && !rec.Told:
			q.tell(ctx, rec)
		}
	}
	q.prune()
	return nil
}

// mine reports whether this process queued key.
func (q *BuildQueue) mine(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, k := range q.busy {
		if k == key {
			return true
		}
	}
	return false
}

// Outcome is the record of the build key, or ErrNoBuildJob.
func (q *BuildQueue) Outcome(_ context.Context, key string) (BuildOutcome, error) {
	rec, err := q.load(key)
	if err != nil {
		return BuildOutcome{}, err
	}
	return rec.outcome(), nil
}

// recordPath is the file of key's record: the key holds a module name and a
// call's hash, so its digest names the file.
func (q *BuildQueue) recordPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(q.d.Dir, hex.EncodeToString(sum[:16])+".json")
}

// save writes rec's record through a temporary file and a rename.
func (q *BuildQueue) save(rec jobRecord) error {
	if err := os.MkdirAll(q.d.Dir, 0o700); err != nil {
		return fmt.Errorf("builtin: build records: %w", err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := writeFile(q.recordPath(rec.Key), raw); err != nil {
		return fmt.Errorf("builtin: record of %s: %w", rec.Key, err)
	}
	return nil
}

// maxRecord caps a record file's bytes: an answer holds at most a log's end.
const maxRecord = 1 << 20

// load reads key's record; ErrNoBuildJob when there is none.
func (q *BuildQueue) load(key string) (jobRecord, error) {
	rec, err := readRecord(q.recordPath(key))
	if errors.Is(err, fs.ErrNotExist) {
		return jobRecord{}, ErrNoBuildJob
	}
	if err != nil {
		return jobRecord{}, err
	}
	if rec.Key != key {
		return jobRecord{}, ErrNoBuildJob
	}
	return rec, nil
}

// readRecord reads one record file.
func readRecord(path string) (jobRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return jobRecord{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := readCapped(f, maxRecord)
	if err != nil {
		return jobRecord{}, fmt.Errorf("builtin: %s: %w", filepath.Base(path), err)
	}
	var rec jobRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return jobRecord{}, fmt.Errorf("builtin: %s: %w", filepath.Base(path), err)
	}
	return rec, nil
}

// list reads every record; none when the folder is missing. A record that
// cannot be read is skipped and logged.
func (q *BuildQueue) list() ([]jobRecord, error) {
	entries, err := os.ReadDir(q.d.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("builtin: build records: %w", err)
	}
	var out []jobRecord
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := readRecord(filepath.Join(q.d.Dir, e.Name()))
		if err != nil {
			q.d.Log.Warn("builds: a build record cannot be read", "file", e.Name(), "err", err)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// prune removes the records of builds told over keepBuildRecords ago.
func (q *BuildQueue) prune() {
	recs, err := q.list()
	if err != nil {
		q.d.Log.Warn("builds: old build records were not pruned", "err", err)
		return
	}
	cut := q.d.Now().Add(-keepBuildRecords).UnixMilli()
	for _, rec := range recs {
		if rec.Told && !rec.open() && rec.Ended < cut {
			if err := os.Remove(q.recordPath(rec.Key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				q.d.Log.Warn("builds: an old build record was not removed", "key", rec.Key, "err", err)
			}
		}
	}
}

// logErr logs err unless the node is stopping.
func (q *BuildQueue) logErr(ctx context.Context, msg string, err error, args ...any) {
	if ctx.Err() != nil {
		return
	}
	q.d.Log.Error(msg, append(args, "err", err)...)
}
