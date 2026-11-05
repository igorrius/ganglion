// Package plan shows a person the steps of the work under way: one message per
// conversation, sent during the turn and edited in place (R-LIVE-8, ADR-0052).
// It displays and approves nothing: policies decide per call. A plan is
// progress, not the reply, so it enters the outbox at once, outside the turn's
// transaction (R-DLV-4), and it belongs to the conversation, so it outlives a
// press's new turn. A conversation has one turn at a time (R-AGT-1), so one
// plan row has one writer.
package plan

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Status is where one step stands.
type Status string

// Statuses of a step.
const (
	Waiting Status = "waiting"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
	Skipped Status = "skipped"
)

// marks are the statuses as a person sees them.
var marks = map[Status]string{Waiting: "·", Running: "⏳", Done: "✅", Failed: "❌", Skipped: "⏭"}

// Limits. A call past them returns an error the model reads.
const (
	maxSteps     = 20
	maxLineRunes = 200
	maxTextUnits = 3500 // UTF-16 units, so a plan always fits one message
	// TTL is how long an untouched plan stays active (the M3d design, 7.2).
	TTL = time.Hour
	// editEvery spaces out edits, for Telegram's limits and against flicker.
	editEvery = 2 * time.Second
)

// Errors a call can end with.
var (
	// ErrLimit is a plan or a step past the limits, or one that makes no sense.
	ErrLimit = errors.New("plan: past the limits")
	// ErrNoPlan is a step in a conversation with no active plan.
	ErrNoPlan = errors.New("plan: no active plan in this conversation")
)

// Step is one line of a plan.
type Step struct {
	Text   string `json:"text"`
	Status Status `json:"status"`
	Note   string `json:"note,omitempty"`
}

// Plan is what a person sees, and where it goes.
type Plan struct {
	Transport string `json:"transport"`
	Steps     []Step `json:"steps"`
	Outcome   string `json:"outcome,omitempty"`
}

// Render is the plan's message.
func Render(p Plan) string {
	lines := make([]string, 0, len(p.Steps))
	for i, s := range p.Steps {
		line := marks[s.Status] + " " + strconv.Itoa(i+1) + ". " + s.Text
		if s.Note != "" {
			line += ": " + s.Note
		}
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")
	if p.Outcome != "" {
		text += "\n\n" + p.Outcome
	}
	return text
}

// Outbox is where the plan's messages go (queue.Outbox).
type Outbox interface {
	Enqueue(ctx context.Context, msgs ...event.Outgoing) error
}

// Store keeps the conversations' plans.
type Store struct {
	db    *sql.DB
	box   Outbox
	now   func() time.Time
	shown metric.Int64Counter
	edits metric.Int64Counter
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New migrates the "plan" component and returns the store.
func New(ctx context.Context, db *sql.DB, box Outbox, opts ...Option) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "plan", sub); err != nil {
		return nil, err
	}
	m := otel.Meter("ganglion/plan")
	shown, err := m.Int64Counter("ganglion.plan.shown", metric.WithDescription("Plans shown as a new message."))
	if err != nil {
		return nil, err
	}
	edits, err := m.Int64Counter("ganglion.plan.edits", metric.WithDescription("Changes of a shown plan, by result: sent, coalesced."))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, box: box, now: time.Now, shown: shown, edits: edits}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// row is a stored plan.
type row struct {
	conv, idem string
	plan       Plan
	version    int
	active     bool
	dirty      bool
	lastEdit   time.Time
	updated    time.Time
}

// showKey is the first message's key, and the plan's way to find it.
func showKey(idem string) string { return "plan:" + idem + ":show" }

// Show shows the steps: a new message, or a change of the plan already active in
// the conversation, whichever call or run shows it. The message is stored first
// under a key from the call, so a crash in between shows it once.
func (s *Store) Show(ctx context.Context, c tool.Call, steps []string) error {
	p, err := newPlan(c.Transport, steps)
	if err != nil {
		return err
	}
	now := s.now()
	r, found, err := s.load(ctx, c.Conv, now)
	if err != nil {
		return err
	}
	if found {
		r.plan = p
		return s.changed(ctx, r, now)
	}
	r = row{conv: c.Conv, idem: c.Key(), plan: p, version: 1, active: true, lastEdit: now}
	if err := s.box.Enqueue(ctx, event.Outgoing{IdemKey: showKey(r.idem), Transport: p.Transport, Conv: c.Conv, Text: Render(p)}); err != nil {
		return fmt.Errorf("plan: show: %w", err)
	}
	s.shown.Add(ctx, 1)
	return s.save(ctx, r, now)
}

// newPlan checks the steps and makes a plan with the first one running. A step
// is one line: a line break in it becomes a space.
func newPlan(transport string, steps []string) (Plan, error) {
	if len(steps) == 0 || len(steps) > maxSteps {
		return Plan{}, fmt.Errorf("%w: a plan has 1 to %d steps, this one %d", ErrLimit, maxSteps, len(steps))
	}
	p := Plan{Transport: transport, Steps: make([]Step, len(steps))}
	for i, text := range steps {
		text = oneLine(text)
		if text == "" || utf8.RuneCountInString(text) > maxLineRunes {
			return Plan{}, fmt.Errorf("%w: step %d must have 1 to %d characters", ErrLimit, i+1, maxLineRunes)
		}
		p.Steps[i] = Step{Text: text, Status: Waiting}
	}
	p.Steps[0].Status = Running
	if units := len(utf16.Encode([]rune(Render(p)))); units > maxTextUnits {
		return Plan{}, fmt.Errorf("%w: the plan is %d units, at most %d", ErrLimit, units, maxTextUnits)
	}
	return p, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// closeUpTo marks what the end of step n means for the others: the lines before
// it that nobody closed are behind it, and the next one starts. A model often
// ticks only some steps, and work it did must not read as skipped.
func (p *Plan) closeUpTo(n int) {
	for i := range n - 1 {
		if p.Steps[i].Status == Waiting || p.Steps[i].Status == Running {
			p.Steps[i].Status = Done
		}
	}
	if n < len(p.Steps) && p.Steps[n].Status == Waiting {
		p.Steps[n].Status = Running
	}
}

// Step marks step n, from 1; a step that ended starts the next one waiting.
// outcome, when given, is the plan's last line.
func (s *Store) Step(ctx context.Context, conv string, n int, st Status, note, outcome string) error {
	note, outcome = oneLine(note), oneLine(outcome)
	if _, ok := marks[st]; !ok || utf8.RuneCountInString(note) > maxLineRunes || utf8.RuneCountInString(outcome) > maxLineRunes {
		return fmt.Errorf("%w: status %q, or a note or an outcome over %d characters", ErrLimit, st, maxLineRunes)
	}
	now := s.now()
	r, found, err := s.load(ctx, conv, now)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoPlan
	}
	if n < 1 || n > len(r.plan.Steps) {
		return fmt.Errorf("%w: step %d of %d", ErrLimit, n, len(r.plan.Steps))
	}
	r.plan.Steps[n-1].Status, r.plan.Steps[n-1].Note = st, note
	if st != Waiting && st != Running {
		r.plan.closeUpTo(n)
	}
	if outcome != "" {
		r.plan.Outcome = outcome
	}
	if units := len(utf16.Encode([]rune(Render(r.plan)))); units > maxTextUnits {
		return fmt.Errorf("%w: the plan would be %d units, at most %d", ErrLimit, units, maxTextUnits)
	}
	return s.changed(ctx, r, now)
}

// Finish ends the turn's part in the plan. kept leaves its lines as they are,
// for a press to go on (a parked call); otherwise lines not finished become ⏭
// and the plan closes. Either way what changed goes out now, past the throttle.
// A conversation with no active plan is nothing to do.
func (s *Store) Finish(ctx context.Context, conv string, kept bool) error {
	now := s.now()
	r, found, err := s.load(ctx, conv, now)
	if err != nil || !found {
		return err
	}
	if !kept {
		r.active = false
		for i, st := range r.plan.Steps {
			if st.Status == Waiting || st.Status == Running {
				r.plan.Steps[i].Status, r.dirty = Skipped, true
			}
		}
	}
	if !r.dirty {
		return s.save(ctx, r, now)
	}
	r.version++
	return s.flush(ctx, r, now, true)
}

// changed stores a change and sends it, unless an edit went out just now: then
// it waits for the next step or the end, and the edit carries them all.
func (s *Store) changed(ctx context.Context, r row, now time.Time) error {
	r.version++
	r.dirty = true
	return s.flush(ctx, r, now, false)
}

// flush sends the plan as an edit when it may, and stores the row. The edit names
// the first message by key, so it needs no delivered reference: the sender finds
// it in its turn, and the end of a turn never waits for a delivery.
func (s *Store) flush(ctx context.Context, r row, now time.Time, final bool) error {
	if !final && now.Sub(r.lastEdit) < editEvery {
		s.edits.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "coalesced")))
		return s.save(ctx, r, now)
	}
	key := "plan:" + r.idem + ":v" + strconv.Itoa(r.version)
	m := event.Outgoing{IdemKey: key, Transport: r.plan.Transport, Conv: r.conv, Text: Render(r.plan), EditOf: showKey(r.idem)}
	if err := s.box.Enqueue(ctx, m); err != nil {
		return fmt.Errorf("plan: edit: %w", err)
	}
	s.edits.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "sent")))
	r.dirty, r.lastEdit = false, now
	return s.save(ctx, r, now)
}

// load returns the conversation's active plan; one past its TTL counts as closed.
func (s *Store) load(ctx context.Context, conv string, now time.Time) (row, bool, error) {
	r := row{conv: conv}
	var (
		raw                string
		active, dirty      int
		lastEdit, updateMS int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT idem_key, steps, version, active, dirty, last_edit_ms, updated_ms FROM plans WHERE conv = ?`, conv).
		Scan(&r.idem, &raw, &r.version, &active, &dirty, &lastEdit, &updateMS)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("plan: load: %w", err)
	}
	r.active, r.dirty, r.lastEdit, r.updated = active != 0, dirty != 0, time.UnixMilli(lastEdit), time.UnixMilli(updateMS)
	if !r.active || now.Sub(r.updated) > TTL {
		return row{}, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &r.plan); err != nil {
		return row{}, false, fmt.Errorf("plan: load: %w", err)
	}
	return r, true, nil
}

// save stores the row, in its own transaction: the plan is shown as it is
// written, not with the turn.
func (s *Store) save(ctx context.Context, r row, now time.Time) error {
	raw, err := json.Marshal(r.plan)
	if err != nil {
		return fmt.Errorf("plan: save: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO plans (conv, idem_key, steps, version, active, dirty, last_edit_ms, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (conv) DO UPDATE SET idem_key = excluded.idem_key, steps = excluded.steps, version = excluded.version,
		  active = excluded.active, dirty = excluded.dirty, last_edit_ms = excluded.last_edit_ms, updated_ms = excluded.updated_ms`,
		r.conv, r.idem, string(raw), r.version, boolInt(r.active), boolInt(r.dirty), r.lastEdit.UnixMilli(), now.UnixMilli()); err != nil {
		return fmt.Errorf("plan: save: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
