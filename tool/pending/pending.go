// Package pending keeps tool calls that wait for the owner's confirmation
// and the buttons that answer them (design section 5.2, ADR-0032). A call
// the policy sends for confirmation is stored with a request cell
// (ADR-0014) and two one-time buttons; a press, or the expiry, becomes a
// new event in the call's conversation, which runs or drops the call and
// resumes the turn. Quick-action buttons (Delete, Details) are stored the
// same way, without a call. Everything is in SQLite, so a button made
// before a restart works after it.
package pending

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DefaultTTL is how long the owner has to answer (R-POL-2).
const DefaultTTL = 15 * time.Minute

// Reoffer is how long after its expiry an expired call whose outcome has
// not reached its conversation is offered again by ExpireDue. It is far
// below the inbox's retention, so an event that failed every attempt is
// not stored anew once the inbox has forgotten its key.
const Reoffer = 24 * time.Hour

// Status is where a pending call is. Statuses are stored, so their values
// never change.
type Status string

// Statuses. Approved, denied and expired are decided but not yet handed
// to the conversation; done means the outcome is in Result and the turn
// that resumed the conversation has it.
const (
	StatusWaiting  Status = "waiting"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusExpired  Status = "expired"
	StatusDone     Status = "done"
)

// Actions of the confirmation buttons; any other action is a quick action
// registered by a tool (e.g. "schedule.delete").
const (
	ActionApprove = "approve"
	ActionDeny    = "deny"
)

// Labels of the confirmation buttons.
const (
	labelApprove = "Approve"
	labelDeny    = "Deny"
)

// Errors of Press; the agent decides on them with errors.Is.
var (
	// ErrUsed is a press of a button that was pressed before, or whose
	// question was answered with the other button.
	ErrUsed = errors.New("pending: button already used")
	// ErrExpired is a press after the button's expiry.
	ErrExpired = errors.New("pending: button expired")
	// ErrNotYours is a press by someone the button was not made for, or
	// in another conversation.
	ErrNotYours = errors.New("pending: button belongs to someone else")
	// ErrNotFound is an unknown button or pending call.
	ErrNotFound = errors.New("pending: not found")
)

// Item is a call awaiting confirmation.
type Item struct {
	ID, Conv, Transport, Principal, TurnID string
	N                                      int
	Tool                                   string
	Args                                   json.RawMessage
	Question                               string
	Status                                 Status
	Expires                                time.Time
	Result                                 string
}

// Call rebuilds the stored call with its original turn and number, so its
// key, the idempotency key of its side effects, is the original one
// (R-DLV-6). The caller sets the parts that are decided at run time
// (Admin, Tainted).
func (i Item) Call() tool.Call {
	return tool.Call{TurnID: i.TurnID, N: i.N, Conv: i.Conv, Transport: i.Transport, Principal: i.Principal, Name: i.Tool, Args: i.Args}
}

// Button is a stored button. Only ID goes to the chat.
type Button struct {
	ID      string
	Pending string // the pending call; "" for a quick action
	Action  string // ActionApprove, ActionDeny or a quick action's name
	Label   string
	// Principal is the person the button was made for; Conv is where.
	Principal, Conv string
	Args            json.RawMessage // a quick action's arguments
	Expires         time.Time
	Used            bool
}

// Press is a valid press: the button and, for a confirmation, its call.
type Press struct {
	Button Button
	Item   Item // zero for a quick action
}

// By is who pressed a button, where, and the event that carries the
// press. The same event pressing again (its turn reruns) gets the same
// press back.
type By struct {
	Principal, Conv, Event string
}

// Store keeps pending calls and buttons. It implements policy.Asker.
type Store struct {
	db    *sql.DB
	cells *cells.Service
	now   func() time.Time
	ttl   time.Duration
}

// New migrates the "pending" component. ttl is how long a question and
// its buttons stay valid (DefaultTTL).
func New(ctx context.Context, db *sql.DB, cs *cells.Service, now func() time.Time, ttl time.Duration) (*Store, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("pending: ttl %v must be positive", ttl)
	}
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "pending", sub); err != nil {
		return nil, err
	}
	return &Store{db: db, cells: cs, now: now, ttl: ttl}, nil
}

// NewButtonID returns a button id: 16 random bytes in Crockford base32,
// 26 characters, so "c:"+id fits Telegram's 64-byte callback data.
func NewButtonID() string { return event.RandomID() }

// cellID is the request cell of pending call id.
func cellID(id string) string { return "request:call:" + id }

// Ask implements policy.Asker: it stores the call, two buttons (approve,
// deny) for the call's principal and a request cell
// "request:call:<id>", and returns the id. The same call key asked again
// (a turn rerun after a crash) returns the stored id. It never returns an
// empty id without an error.
func (s *Store) Ask(ctx context.Context, c tool.Call, question string) (string, error) {
	if c.TurnID == "" || c.Principal == "" || c.Conv == "" || question == "" {
		return "", fmt.Errorf("pending: ask %s: a call needs a turn, a principal, a conversation and a question", c.Name)
	}
	now := s.now().UTC()
	it := Item{ID: event.NewID(now), Conv: c.Conv, Transport: c.Transport, Principal: c.Principal, TurnID: c.TurnID, N: c.N,
		Tool: c.Name, Args: c.Args, Question: question, Status: StatusWaiting, Expires: now.Add(s.ttl)}
	id, err := s.insert(ctx, it, now)
	if err != nil {
		return "", fmt.Errorf("pending: ask %s: %w", c.Key(), err)
	}
	payload, err := json.Marshal(struct {
		Tool string `json:"tool"`
		Call string `json:"call"`
	}{c.Name, c.Key()})
	if err != nil {
		return "", fmt.Errorf("pending: ask %s: %w", c.Key(), err)
	}
	_, err = s.cells.AskAs(ctx, cellID(id), cells.Request{Question: question, Payload: payload}, s.ttl, "agent")
	if err != nil && !errors.Is(err, cells.ErrExists) {
		return "", fmt.Errorf("pending: ask %s: request cell: %w", c.Key(), err)
	}
	return id, nil
}

// insert stores it with its two buttons, unless its call key is stored
// already; it returns the stored id.
func (s *Store) insert(ctx context.Context, it Item, now time.Time) (string, error) {
	var id string
	err := s.tx(ctx, func(tx *sql.Conn) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO pending_calls (id, turn_id, n, conv, transport, principal, tool, args, question,
			status, created_ms, expires_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (turn_id, n) DO NOTHING`,
			it.ID, it.TurnID, it.N, it.Conv, it.Transport, it.Principal, it.Tool, string(it.Args), it.Question,
			string(it.Status), now.UnixMilli(), it.Expires.UnixMilli())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return tx.QueryRowContext(ctx, `SELECT id FROM pending_calls WHERE turn_id = ? AND n = ?`, it.TurnID, it.N).Scan(&id)
		}
		id = it.ID
		for _, b := range []Button{
			{Action: ActionApprove, Label: labelApprove},
			{Action: ActionDeny, Label: labelDeny},
		} {
			b.ID, b.Pending, b.Principal, b.Conv, b.Expires = NewButtonID(), it.ID, it.Principal, it.Conv, it.Expires
			if err := insertButton(ctx, tx, b); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// AddButtons stores quick-action buttons (Pending ""), made by a tool
// for a message it sends. Each needs an id (NewButtonID), an action, a
// principal, a conversation and an expiry.
func (s *Store) AddButtons(ctx context.Context, bs ...Button) error {
	for _, b := range bs {
		if b.ID == "" || b.Action == "" || b.Principal == "" || b.Conv == "" || b.Expires.IsZero() {
			return fmt.Errorf("pending: add button %q: it needs an id, an action, a principal, a conversation and an expiry", b.ID)
		}
	}
	err := s.tx(ctx, func(tx *sql.Conn) error {
		for _, b := range bs {
			if err := insertButton(ctx, tx, b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("pending: add buttons: %w", err)
	}
	return nil
}

func insertButton(ctx context.Context, tx *sql.Conn, b Button) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO pending_buttons (id, pending, action, label, principal, conv, args, expires_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, b.ID, b.Pending, b.Action, b.Label, b.Principal, b.Conv, string(b.Args), b.Expires.UnixMilli())
	return err
}

// Outgoing is the question of pending call id with its Approve and Deny
// buttons, addressed to the call's conversation. Its IdemKey is
// "<call key>:confirm", so a rerun turn sends it once.
func (s *Store) Outgoing(ctx context.Context, id string) (event.Outgoing, error) {
	it, err := s.Get(ctx, id)
	if err != nil {
		return event.Outgoing{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, label FROM pending_buttons WHERE pending = ? ORDER BY rowid`, id)
	if err != nil {
		return event.Outgoing{}, fmt.Errorf("pending: buttons of %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var buttons []event.Button
	for rows.Next() {
		var b event.Button
		if err := rows.Scan(&b.ID, &b.Label); err != nil {
			return event.Outgoing{}, fmt.Errorf("pending: buttons of %s: %w", id, err)
		}
		buttons = append(buttons, b)
	}
	if err := rows.Err(); err != nil {
		return event.Outgoing{}, fmt.Errorf("pending: buttons of %s: %w", id, err)
	}
	return event.Outgoing{IdemKey: it.Call().Key() + ":confirm", Transport: it.Transport, Conv: it.Conv, Text: it.Question, Buttons: buttons}, nil
}

// Get returns pending call id; ErrNotFound when there is none.
func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM pending_calls WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, fmt.Errorf("pending: call %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Item{}, fmt.Errorf("pending: call %s: %w", id, err)
	}
	return it, nil
}

// Decide sets the status and the result of pending call id.
func (s *Store) Decide(ctx context.Context, id string, st Status, result string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pending_calls SET status = ?, result = ? WHERE id = ?`, string(st), result, id)
	if err != nil {
		return fmt.Errorf("pending: decide %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("pending: decide %s: %w", id, ErrNotFound)
	}
	return nil
}

// Waiting returns those of ids whose calls still wait for an answer, in
// their order; ids of answered, expired or unknown calls are left out.
func (s *Store) Waiting(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, string(StatusWaiting))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM pending_calls WHERE status = ? AND id IN (?`+
		strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("pending: waiting: %w", err)
	}
	defer func() { _ = rows.Close() }()
	waiting := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pending: waiting: %w", err)
		}
		waiting[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending: waiting: %w", err)
	}
	var out []string
	for _, id := range ids {
		if waiting[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// ExpireDue marks waiting calls past their expiry as expired, expires
// their request cells, and returns the expired calls whose outcome has
// not reached their conversation yet (status expired, not done) and that
// expired within Reoffer: the newly expired ones and any whose event was
// not stored before.
func (s *Store) ExpireDue(ctx context.Context) ([]Item, error) {
	now := s.now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE pending_calls SET status = ? WHERE status = ? AND expires_ms <= ?`,
		string(StatusExpired), string(StatusWaiting), now); err != nil {
		return nil, fmt.Errorf("pending: expire: %w", err)
	}
	if _, err := s.cells.ExpireDue(ctx); err != nil {
		return nil, fmt.Errorf("pending: expire request cells: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM pending_calls WHERE status = ? AND expires_ms > ?
		ORDER BY expires_ms, id`, string(StatusExpired), now-Reoffer.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("pending: expire: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("pending: expire: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

const itemColumns = `id, conv, transport, principal, turn_id, n, tool, args, question, status, expires_ms, result`

type scanner interface{ Scan(dest ...any) error }

func scanItem(r scanner) (Item, error) {
	var it Item
	var args, status string
	var expires int64
	if err := r.Scan(&it.ID, &it.Conv, &it.Transport, &it.Principal, &it.TurnID, &it.N, &it.Tool, &args, &it.Question,
		&status, &expires, &it.Result); err != nil {
		return Item{}, err
	}
	it.Args, it.Status, it.Expires = json.RawMessage(args), Status(status), time.UnixMilli(expires).UTC()
	return it, nil
}

const buttonColumns = `id, pending, action, label, principal, conv, args, expires_ms, used, used_by`

// scanButton reads a button and the event that pressed it.
func scanButton(r scanner) (Button, string, error) {
	var b Button
	var args, usedBy string
	var expires int64
	if err := r.Scan(&b.ID, &b.Pending, &b.Action, &b.Label, &b.Principal, &b.Conv, &args, &expires, &b.Used, &usedBy); err != nil {
		return Button{}, "", err
	}
	if args != "" {
		b.Args = json.RawMessage(args)
	}
	b.Expires = time.UnixMilli(expires).UTC()
	return b, usedBy, nil
}

// tx runs fn in an immediate transaction (sqlitedb.Immediate): two
// presses of one button cannot both see it unused.
func (s *Store) tx(ctx context.Context, fn func(c *sql.Conn) error) error {
	return sqlitedb.Immediate(ctx, s.db, fn)
}
