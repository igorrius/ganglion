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
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
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

// card is an open job and its card. Its row is guarded by Registry.mu, which
// is never held across I/O; io orders the job's row writes and messages, so
// its versions go up one at a time and no version is sent twice.
type card struct {
	io   sync.Mutex
	row  row
	gone bool // ended or dropped: no further change
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

// cardless reports whether conv is one no person reads, a scheduled task's:
// its jobs are kept and listed, but get no card.
func cardless(conv string) bool { return strings.HasPrefix(conv, tool.TaskConvPrefix) }

// Start registers j, stores its row and then sends its card. j's State is
// running (the default) or queued, and Started defaults to now. A key seen
// before sends no second card: a queued job started again as running begins
// to run, its card edited at once and its time counted from now; any other
// repeat is a no-op, so a job has one card for its key, across a restart too.
// A card the outbox refused is sent at the next tick; the job is kept.
func (r *Registry) Start(ctx context.Context, j Job) error {
	j, err := r.complete(j)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if c, ok := r.live[j.Key]; ok {
		r.mu.Unlock()
		return r.promote(ctx, c, j)
	}
	c := &card{row: row{job: j}}
	c.io.Lock() // a second Start of the key waits for this one
	r.live[j.Key] = c
	r.mu.Unlock()
	if err := r.register(ctx, c, row{job: j}); err != nil {
		c.io.Unlock()
		return err
	}
	c.io.Unlock()
	return r.update(ctx, c, func(*row) {})
}

// register stores first, c's first row; a key that has a row already, or a
// row that cannot be stored, drops c.
func (r *Registry) register(ctx context.Context, c *card, first row) error {
	known, err := r.d.Store.known(ctx, first.job.Key)
	if err == nil && !known {
		err = r.d.Store.insert(ctx, first)
		if err == nil {
			return nil
		}
	}
	r.drop(c)
	return err
}

// drop takes c off the live jobs for good.
func (r *Registry) drop(c *card) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.gone = true
	if r.live[c.row.job.Key] == c {
		delete(r.live, c.row.job.Key)
	}
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
	if j.State != StateRunning {
		return nil
	}
	now := r.d.Now()
	return r.update(ctx, c, func(n *row) {
		if n.job.State == StateQueued {
			n.job.State, n.job.Started = StateRunning, now
		}
	})
}

// Progress sets a job's count; its card shows it at the next tick. An
// unknown or ended key is ignored.
func (r *Registry) Progress(key string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.live[key]; ok {
		c.row.job.Count = count
	}
}

// End ends a job as s (done, failed or interrupted) and edits its card to its
// last text; no edit follows. Once its row is stored as ended the job is no
// longer open, even when the outbox refused the edit (the error says so). A
// key that is not open here is a no-op.
func (r *Registry) End(ctx context.Context, key string, s State) error {
	if s.open() || s == "" {
		return fmt.Errorf("%w: end %s as %q", ErrInvalid, key, s)
	}
	r.mu.Lock()
	c, ok := r.live[key]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	now := r.d.Now()
	return r.update(ctx, c, func(n *row) { n.job.State, n.job.Ended = s, now })
}

// update applies change to c's row and stores it, and sends what the card
// needs: its first message while none is in the outbox (before the row, which
// already exists), else an edit when its text changed (after the row, so a
// restart never edits a card back). A job that ends leaves the live jobs once
// its row is stored. The error is a row that was not stored or a message the
// outbox refused.
func (r *Registry) update(ctx context.Context, c *card, change func(*row)) error {
	c.io.Lock()
	defer c.io.Unlock()
	r.mu.Lock()
	if c.gone {
		r.mu.Unlock()
		return nil
	}
	cur := c.row
	r.mu.Unlock()
	next := cur
	change(&next)
	text := render(r.speaker(ctx, next.job.Principal), next.job, r.d.Now())
	msg, sendErr := r.plan(ctx, cur, &next, text)
	if next == cur {
		r.tel.edit(ctx, editUnchanged)
		return sendErr
	}
	if err := r.d.Store.save(ctx, next); err != nil {
		r.tel.edit(ctx, editFailed)
		return err
	}
	r.apply(ctx, c, cur, next)
	if msg == nil {
		return sendErr
	}
	if err := r.d.Outbox.Enqueue(ctx, *msg); err != nil {
		r.tel.edit(ctx, editFailed)
		return fmt.Errorf("jobs: edit the card of %s: %w", next.job.Key, err)
	}
	r.tel.edit(ctx, editSent)
	return nil
}

// plan decides the card's message for next, whose text is text: the first
// message is sent here, before the row is stored; an edit is returned, to be
// sent after it. A task's job has no card.
func (r *Registry) plan(ctx context.Context, cur row, next *row, text string) (*event.Outgoing, error) {
	j := next.job
	switch {
	case cardless(j.Conv):
		return nil, nil
	case cur.shown == "":
		err := r.d.Outbox.Enqueue(ctx, event.Outgoing{IdemKey: cardKey(j.Key), Transport: j.Transport, Conv: j.Conv,
			Text: text, Silent: true})
		if err != nil {
			r.tel.edit(ctx, editFailed)
			return nil, fmt.Errorf("jobs: card of %s: %w", j.Key, err)
		}
		r.tel.edit(ctx, editSent)
		next.shown = text
		return nil, nil
	case text != cur.shown:
		next.version, next.shown = cur.version+1, text
		return &event.Outgoing{IdemKey: editKey(j.Key, next.version), EditOf: cardKey(j.Key), Transport: j.Transport,
			Conv: j.Conv, Text: text, Silent: true}, nil
	}
	return nil, nil
}

// apply makes next c's row, keeping a count set since cur was read; a job
// that ended leaves the live jobs.
func (r *Registry) apply(ctx context.Context, c *card, cur, next row) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := c.row.job.Count
	c.row = next
	if count != cur.job.Count {
		c.row.job.Count = count
	}
	if next.job.State.open() {
		return
	}
	c.gone = true
	if r.live[next.job.Key] == c {
		delete(r.live, next.job.Key)
	}
	if cur.job.State.open() {
		r.tel.end(ctx, next.job.Kind, next.job.State)
	}
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
		if keep(c.row.job) {
			out = append(out, c.row.job)
		}
	}
	slices.SortFunc(out, func(a, b Job) int {
		if c := a.Started.Compare(b.Started); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
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
	now := r.d.Now()
	for _, o := range open {
		r.mu.Lock()
		_, mine := r.live[o.job.Key]
		r.mu.Unlock()
		if mine {
			continue
		}
		c := &card{row: o}
		if err := r.update(ctx, c, func(n *row) { n.job.State, n.job.Ended = StateInterrupted, now }); err != nil {
			r.logErr(ctx, "jobs: an interrupted job's card was not edited", err, "key", o.job.Key)
		}
	}
	r.prune(ctx, now)
	return nil
}

// tick edits each open job's card whose text changed, and sends a first card
// the outbox refused before. The live jobs are read under the lock; their
// I/O runs outside it, so a turn reading Running never waits for it.
func (r *Registry) tick(ctx context.Context) {
	r.mu.Lock()
	cards := make(map[string]*card, len(r.live))
	maps.Copy(cards, r.live)
	r.mu.Unlock()
	for key, c := range cards {
		if err := r.update(ctx, c, func(*row) {}); err != nil {
			r.logErr(ctx, "jobs: a card was not edited", err, "key", key)
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
