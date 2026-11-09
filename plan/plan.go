// Package plan shows a person the steps of the work under way: one message per
// conversation, sent during the turn and edited in place (R-LIVE-8, ADR-0052).
// A plan shown through a pressed question approves exactly the commands its
// steps list, in its conversation, until it closes or an hour after the press
// (R-CMD-7, ADR-0040); the policy and host.exec read that. A plan is
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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
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

// Step is one line of a plan, and the host command it runs, if any.
type Step struct {
	Text    string        `json:"text"`
	Status  Status        `json:"status"`
	Note    string        `json:"note,omitempty"`
	Command *tool.Command `json:"command,omitempty"`
	// Line is Command as the person reads it, in their language. Show writes
	// it from Command, so a caller's value never reaches the chat.
	Line string `json:"line,omitempty"`
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
		if s.Line != "" {
			line += "\n    " + s.Line
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
	home  string
	now   func() time.Time
	shown metric.Int64Counter
	edits metric.Int64Counter
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithHome sets the node user's home, the folder of a host command without
// dir: a step's command is shown in it, and the approved commands a new plan
// still lists are matched with "" read as home (tool.Command.SameAs). The
// default is "", which shows no folder for such a command.
func WithHome(home string) Option { return func(s *Store) { s.home = home } }

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
	msgKey     string // the question the plan was approved through; "" is its first message
	plan       Plan
	approved   []tool.Command
	approvedAt time.Time
	version    int
	active     bool
	dirty      bool
	lastEdit   time.Time
	updated    time.Time
}

// showKey is the first message's key, and the plan's way to find it.
func showKey(idem string) string { return "plan:" + idem + ":show" }

// messageKey is the key of the message the plan edits: its first message, or
// the question it was approved through.
func (r row) messageKey() string {
	if r.msgKey != "" {
		return r.msgKey
	}
	return showKey(r.idem)
}

// Show shows the steps. A call the owner approved (its question was this
// plan) makes the plan anew with that question as its message and approves the
// commands that need a press; any other call shows them on the active plan or a
// new message and keeps only the approved commands the new steps still list
// (R-CMD-7, ADR-0040). A new message is stored first under a key from the call,
// so a crash in between shows it once.
func (s *Store) Show(ctx context.Context, c tool.Call, steps []Step) error {
	p, err := newPlan(c.Speak, c.Transport, s.home, steps)
	if err != nil {
		return err
	}
	now := s.now()
	r, found, err := s.load(ctx, c.Conv, now)
	if err != nil {
		return err
	}
	if c.Approved {
		return s.showApproved(ctx, c, p, r, found, now)
	}
	if found {
		r.plan, r.approved = p, stillListed(r.approved, p, s.home)
		return s.changed(ctx, r, now)
	}
	r = row{conv: c.Conv, idem: c.Key(), plan: p, version: 1, active: true, lastEdit: now}
	if err := s.box.Enqueue(ctx, event.Outgoing{IdemKey: showKey(r.idem), Transport: p.Transport, Conv: c.Conv, Text: Render(p)}); err != nil {
		return fmt.Errorf("plan: show: %w", err)
	}
	s.shown.Add(ctx, 1)
	return s.save(ctx, r, now)
}

// showApproved closes a plan shown before, then makes this one on the pressed
// question. A rerun of the same call finds its own row and stores the same
// edit again, which the outbox keeps once.
func (s *Store) showApproved(ctx context.Context, c tool.Call, p Plan, old row, found bool, now time.Time) error {
	if found && old.idem != c.Key() {
		if err := s.close(ctx, old, now); err != nil {
			return err
		}
	}
	r := row{conv: c.Conv, idem: c.Key(), msgKey: c.Key() + pending.QuestionKeySuffix, plan: p, version: 1, active: true,
		approved: needPress(p), approvedAt: now}
	return s.flush(ctx, r, now, true)
}

// needPress lists the plan's commands that need the owner's press: the ones
// its approval is for.
func needPress(p Plan) []tool.Command {
	var out []tool.Command
	for _, st := range p.Steps {
		if st.Command != nil && hostcmd.Classify(st.Command.Argv).Class.NeedsPress() {
			out = append(out, *st.Command)
		}
	}
	return out
}

// stillListed keeps the approved commands that p still lists, argv and folder
// alike, "" read as home: a new plan approves nothing new.
func stillListed(approved []tool.Command, p Plan, home string) []tool.Command {
	return slices.DeleteFunc(slices.Clone(approved), func(a tool.Command) bool {
		return !slices.ContainsFunc(p.Steps, func(st Step) bool { return st.Command != nil && st.Command.SameAs(a, home) })
	})
}

// Approved returns the commands the conversation's approved plan lists: none
// when it has no active plan, its plan was not approved, or the press was more
// than TTL ago, however the plan moved since (R-CMD-7). A clock behind the
// press approves nothing either.
func (s *Store) Approved(ctx context.Context, conv string) ([]tool.Command, error) {
	now := s.now()
	r, found, err := s.load(ctx, conv, now)
	if err != nil || !found {
		return nil, err
	}
	if age := now.Sub(r.approvedAt); age < 0 || age > TTL {
		return nil, nil
	}
	return slices.Clone(r.approved), nil
}

// Check reports whether steps make a plan Show would take: 1 to 20 steps, each
// with text of 1 to 200 characters, the whole message within 3500 UTF-16
// units with each command written out for sp's person in its folder. plan.show
// checks it before its question, so a plan past the limits is refused before
// the owner is asked.
func (s *Store) Check(sp texts.Speaker, transport string, steps []Step) error {
	return Check(sp, transport, s.home, steps)
}

// Check is Store.Check for a store whose home is home.
func Check(sp texts.Speaker, transport, home string, steps []Step) error {
	_, err := newPlan(sp, transport, home, steps)
	return err
}

// newPlan checks the steps and makes a plan with the first one running. A step
// is one line: a line break in it becomes a space. A command step's command is
// written out for sp's person, in its folder (home when it has none), and
// counts within the plan's size.
func newPlan(sp texts.Speaker, transport, home string, steps []Step) (Plan, error) {
	if len(steps) == 0 || len(steps) > maxSteps {
		return Plan{}, fmt.Errorf("%w: a plan has 1 to %d steps, this one %d", ErrLimit, maxSteps, len(steps))
	}
	p := Plan{Transport: transport, Steps: make([]Step, len(steps))}
	for i, st := range steps {
		text := oneLine(st.Text)
		if text == "" || utf8.RuneCountInString(text) > maxLineRunes {
			return Plan{}, fmt.Errorf("%w: step %d must have 1 to %d characters", ErrLimit, i+1, maxLineRunes)
		}
		p.Steps[i] = Step{Text: text, Status: Waiting}
		if st.Command != nil {
			cmd := tool.Command{Argv: slices.Clone(st.Command.Argv), Dir: st.Command.Dir}
			p.Steps[i].Command, p.Steps[i].Line = &cmd, commandLine(sp, cmd, home)
		}
	}
	p.Steps[0].Status = Running
	if units := len(utf16.Encode([]rune(Render(p)))); units > maxTextUnits {
		return Plan{}, fmt.Errorf("%w: the plan is %d units, at most %d", ErrLimit, units, maxTextUnits)
	}
	return p, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// commandLine is cmd as a person reads it under its step: written by
// hostcmd.Show in a code span a backtick cannot end, in its folder, as a
// command's question shows it: home for a command without dir. With no home
// known the folder is left out.
func commandLine(sp texts.Speaker, cmd tool.Command, home string) string {
	line := text.CodeSpan(hostcmd.Show(cmd.Argv))
	dir := cmd.Dir
	if dir == "" {
		dir = home
	}
	if dir == "" {
		return line
	}
	return sp.Text(texts.CmdIn, texts.Vars{"command": line, "dir": text.CodeSpan(hostcmd.Show([]string{dir}))})
}

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
		return s.close(ctx, r, now)
	}
	if !r.dirty {
		return s.save(ctx, r, now)
	}
	r.version++
	return s.flush(ctx, r, now, true)
}

// close ends the plan: lines not finished become ⏭, what changed goes out now,
// and it approves nothing more.
func (s *Store) close(ctx context.Context, r row, now time.Time) error {
	r.active, r.approved = false, nil
	for i, st := range r.plan.Steps {
		if st.Status == Waiting || st.Status == Running {
			r.plan.Steps[i].Status, r.dirty = Skipped, true
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
	m := event.Outgoing{IdemKey: key, Transport: r.plan.Transport, Conv: r.conv, Text: Render(r.plan), EditOf: r.messageKey()}
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
		raw, approved                  string
		active, dirty                  int
		lastEdit, updateMS, approvedMS int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT idem_key, message_key, steps, approved, approved_ms, version, active, dirty, last_edit_ms, updated_ms
		FROM plans WHERE conv = ?`, conv).
		Scan(&r.idem, &r.msgKey, &raw, &approved, &approvedMS, &r.version, &active, &dirty, &lastEdit, &updateMS)
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
	if err := json.Unmarshal([]byte(approved), &r.approved); err != nil {
		return row{}, false, fmt.Errorf("plan: load: %w", err)
	}
	if approvedMS != 0 {
		r.approvedAt = time.UnixMilli(approvedMS)
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
	approved, err := json.Marshal(append([]tool.Command{}, r.approved...))
	if err != nil {
		return fmt.Errorf("plan: save: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO plans (conv, idem_key, message_key, steps, approved, approved_ms, version, active, dirty,
		  last_edit_ms, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (conv) DO UPDATE SET idem_key = excluded.idem_key, message_key = excluded.message_key, steps = excluded.steps,
		  approved = excluded.approved, approved_ms = excluded.approved_ms, version = excluded.version, active = excluded.active,
		  dirty = excluded.dirty, last_edit_ms = excluded.last_edit_ms, updated_ms = excluded.updated_ms`,
		r.conv, r.idem, r.msgKey, string(raw), string(approved), unixMilli(r.approvedAt), r.version, boolInt(r.active), boolInt(r.dirty),
		r.lastEdit.UnixMilli(), now.UnixMilli()); err != nil {
		return fmt.Errorf("plan: save: %w", err)
	}
	return nil
}

// unixMilli is t in Unix milliseconds, 0 for the zero time (no press).
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
