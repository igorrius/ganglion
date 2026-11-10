// Package jobs keeps the node's background jobs: work it runs outside any
// turn, a module build or a host command past its wait (R-LIVE-9, R-CMD-4,
// ADR-0069). Each job has one card in its conversation, a message the node
// sends when the job starts and edits in place every 10 s without a model,
// through the outbox like the plan message (ADR-0052), until it ends as done,
// failed or interrupted by a restart. The card carries only the node's fixed
// words, the job's title and its count. The registry also tells the model and
// /status what runs.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// Kind is what a job does.
type Kind string

// Kinds.
const (
	KindBuild   Kind = "build"   // a module build (R-RUN-9)
	KindCommand Kind = "command" // a host command past its wait (R-CMD-4)
)

// State is where a job stands.
type State string

// States: queued and running are open, the rest are ends.
const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateDone        State = "done"
	StateFailed      State = "failed"
	StateInterrupted State = "interrupted" // the node restarted while it was open
)

// open reports whether s is a state of a job that has not ended.
func (s State) open() bool { return s == StateQueued || s == StateRunning }

// Job is one background job.
type Job struct {
	Key       string // unique; the host job's key or "build:<module>:<id>"
	Kind      Kind
	Conv      string // the conversation it reports to
	Transport string
	Principal string
	Title     string // a module name or an approved argv, as the owner saw it
	Started   time.Time
	Ended     time.Time
	State     State
	Count     int // progress: crates built for a build; 0 when none
}

// TickEvery is how often a running job's card is edited (ruling 14).
const TickEvery = 10 * time.Second

// keepEnded is how long an ended job's row stays.
const keepEnded = 7 * 24 * time.Hour

// pruneEvery is how often old rows are removed while the node runs.
const pruneEvery = time.Hour

// ErrInvalid is a job the registry cannot keep: no key, kind, conversation
// or transport, or a state that does not fit the call.
var ErrInvalid = errors.New("jobs: not a valid job")

// Outbox is where the cards go (queue.Outbox).
type Outbox interface {
	Enqueue(ctx context.Context, msgs ...event.Outgoing) error
}

// LangFunc is a person's language (language.Store.Resolve): a card is in the
// language of the person whose job it is.
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// Deps are what a registry needs. Store and Outbox are required; the rest
// have defaults: English, the shipped texts, the system clock, no log.
type Deps struct {
	Store  *Store
	Outbox Outbox
	Lang   LangFunc
	Texts  *texts.Catalog
	Now    func() time.Time
	After  func(time.Duration) <-chan time.Time
	Log    *slog.Logger
}

// Registry keeps jobs in the node's database and drives their cards. It is a
// node component (Run) and a status source.
type Registry struct {
	d   Deps
	tel telemetry

	mu   sync.Mutex
	live map[string]*card // the open jobs this process started, by key
}

// New returns a registry over d.
func New(d Deps) *Registry {
	if d.Lang == nil {
		d.Lang = func(context.Context, string) (texts.Lang, error) { return texts.English, nil }
	}
	if d.Texts == nil {
		d.Texts = texts.Shipped()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.After == nil {
		d.After = time.After
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	r := &Registry{d: d, live: map[string]*card{}}
	r.tel = newTelemetry(r.openCount)
	return r
}

// cardKey is the key of a job's first message, which its edits name.
func cardKey(key string) string { return "job:" + key + ":card" }

// editKey is the key of a card's n-th edit.
func editKey(key string, n int) string { return cardKey(key) + ":v" + strconv.Itoa(n) }

// Start registers j and sends its card at once. j's State is running (the
// default) or queued, and Started defaults to now. A key seen before sends no
// second card: a queued job started again as running begins to run, its card
// edited at once and its time counted from now; any other repeat is a no-op,
// so a job has one card for its key, across a restart too.
func (r *Registry) Start(ctx context.Context, j Job) error {
	j, err := r.complete(j)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.live[j.Key]; ok {
		return r.promote(ctx, c, j)
	}
	known, err := r.d.Store.known(ctx, j.Key)
	if err != nil || known {
		return err
	}
	c := &card{job: j}
	c.shown = render(r.speaker(ctx, j.Principal), j, r.d.Now())
	if err := r.d.Outbox.Enqueue(ctx, event.Outgoing{IdemKey: cardKey(j.Key), Transport: j.Transport, Conv: j.Conv,
		Text: c.shown, Silent: true}); err != nil {
		return fmt.Errorf("jobs: card of %s: %w", j.Key, err)
	}
	if err := r.d.Store.insert(ctx, *c); err != nil {
		return err
	}
	r.live[j.Key] = c
	return nil
}

// complete checks j and fills its defaults.
func (r *Registry) complete(j Job) (Job, error) {
	if j.State == "" {
		j.State = StateRunning
	}
	if _, kind := icons[j.Kind]; !kind || j.Key == "" || j.Conv == "" || j.Transport == "" || !j.State.open() {
		return Job{}, fmt.Errorf("%w: key %q, kind %q, state %q", ErrInvalid, j.Key, j.Kind, j.State)
	}
	if j.Started.IsZero() {
		j.Started = r.d.Now()
	}
	j.Ended, j.Count = time.Time{}, 0
	return j, nil
}

// promote starts a queued job that is started again as running.
func (r *Registry) promote(ctx context.Context, c *card, j Job) error {
	if c.job.State != StateQueued || j.State != StateRunning {
		return nil
	}
	now := r.d.Now()
	next := *c
	next.job.State, next.job.Started = StateRunning, now
	return r.edit(ctx, c, next, now)
}

// Progress sets a job's count; its card shows it at the next tick. An
// unknown or ended key is ignored.
func (r *Registry) Progress(key string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.live[key]; ok {
		c.job.Count = count
	}
}

// End ends a job as s (done, failed or interrupted) and edits its card to its
// last text; no edit follows. A key that is not open here is a no-op.
func (r *Registry) End(ctx context.Context, key string, s State) error {
	if s.open() || s == "" {
		return fmt.Errorf("%w: end %s as %q", ErrInvalid, key, s)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.live[key]
	if !ok {
		return nil
	}
	now := r.d.Now()
	next := *c
	next.job.State, next.job.Ended = s, now
	if err := r.edit(ctx, c, next, now); err != nil {
		return err
	}
	delete(r.live, key)
	r.tel.end(ctx, c.job.Kind, s)
	return nil
}

// edit makes next the state of c: its row first, then the card's edit when
// its text changed, so a restart never edits a card back. c is left as it was
// when the row could not be stored.
func (r *Registry) edit(ctx context.Context, c *card, next card, now time.Time) error {
	text := render(r.speaker(ctx, next.job.Principal), next.job, now)
	changed := text != c.shown
	if !changed && next.job == c.job {
		r.tel.edit(ctx, editUnchanged)
		return nil // nothing to store: a queued job between ticks
	}
	if changed {
		next.version, next.shown = c.version+1, text
	}
	if err := r.d.Store.save(ctx, next); err != nil {
		r.tel.edit(ctx, editFailed)
		return err
	}
	*c = next
	if !changed {
		r.tel.edit(ctx, editUnchanged)
		return nil
	}
	j := next.job
	if err := r.d.Outbox.Enqueue(ctx, event.Outgoing{IdemKey: editKey(j.Key, next.version), EditOf: cardKey(j.Key),
		Transport: j.Transport, Conv: j.Conv, Text: text, Silent: true}); err != nil {
		r.tel.edit(ctx, editFailed)
		return fmt.Errorf("jobs: edit the card of %s: %w", j.Key, err)
	}
	r.tel.edit(ctx, editSent)
	return nil
}

// Running is the open jobs of conv, running and queued, oldest first: for the
// model's note and /status.
func (r *Registry) Running(conv string) []Job {
	return r.list(func(j Job) bool { return j.Conv == conv })
}

// All is every open job of the node, oldest first, for /status.
func (r *Registry) All() []Job { return r.list(func(Job) bool { return true }) }

// list is the open jobs keep accepts, oldest first.
func (r *Registry) list(keep func(Job) bool) []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Job
	for _, c := range r.live {
		if keep(c.job) {
			out = append(out, c.job)
		}
	}
	slices.SortFunc(out, func(a, b Job) int {
		if c := a.Started.Compare(b.Started); c != 0 {
			return c
		}
		if a.Key < b.Key {
			return -1
		}
		return 1
	})
	return out
}

// openCount is how many jobs are open, for the gauge.
func (r *Registry) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live)
}

// Run ends as interrupted the jobs a stopped node left open, editing their
// cards once, then edits the running jobs' cards every TickEvery until ctx
// ends, and prunes old rows every hour.
func (r *Registry) Run(ctx context.Context) error {
	if err := r.interrupt(ctx); err != nil {
		return err
	}
	pruned := r.d.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.d.After(TickEvery):
		}
		r.tick(ctx)
		if now := r.d.Now(); now.Sub(pruned) >= pruneEvery {
			r.prune(ctx, now)
			pruned = now
		}
	}
}

// interrupt ends the open rows this process did not start: their node
// stopped while they ran or waited.
func (r *Registry) interrupt(ctx context.Context) error {
	open, err := r.d.Store.open(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.d.Now()
	for _, c := range open {
		if _, mine := r.live[c.job.Key]; mine {
			continue
		}
		next := c
		next.job.State, next.job.Ended = StateInterrupted, now
		if err := r.edit(ctx, &c, next, now); err != nil {
			r.logErr(ctx, "jobs: an interrupted job's card was not edited", err, "key", c.job.Key)
			continue
		}
		r.tel.end(ctx, c.job.Kind, StateInterrupted)
	}
	r.prune(ctx, now)
	return nil
}

// tick edits each running job's card whose text changed.
func (r *Registry) tick(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.d.Now()
	for _, c := range r.live {
		if err := r.edit(ctx, c, *c, now); err != nil {
			r.logErr(ctx, "jobs: a card was not edited", err, "key", c.job.Key)
		}
	}
}

// prune removes the rows of jobs that ended over keepEnded ago.
func (r *Registry) prune(ctx context.Context, now time.Time) {
	if err := r.d.Store.prune(ctx, now.Add(-keepEnded)); err != nil {
		r.logErr(ctx, "jobs: old jobs were not pruned", err)
	}
}

// speaker writes in the language of principal; English when it is not known.
func (r *Registry) speaker(ctx context.Context, principal string) texts.Speaker {
	l, err := r.d.Lang(ctx, principal)
	if err != nil {
		r.d.Log.Warn("jobs: language unknown; using English", "err", err)
		l = texts.English
	}
	return texts.Speaker{Lang: l, Cat: r.d.Texts}
}

// logErr logs err unless the node is stopping.
func (r *Registry) logErr(ctx context.Context, msg string, err error, args ...any) {
	if ctx.Err() != nil {
		return
	}
	r.d.Log.Error(msg, append(args, "err", err)...)
}
