// Package journal records every tool call: who asked, what for, what the
// policy decided and how it ended. Arguments and results are redacted and
// capped first, so a secret never reaches the table (design section 4,
// rules 5 and 7).
package journal

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Sizes of what an entry keeps of a call.
const (
	maxArgsBytes = 2048
	maxHeadRunes = 512
	maxErrRunes  = 512
	writeTimeout = 5 * time.Second
)

// DefaultRetain is how long entries are normally kept.
const DefaultRetain = 30 * 24 * time.Hour

// Decision is what the policy decided about a call. Decisions are stored,
// so their values never change.
type Decision string

// Decisions.
const (
	DecisionAllowed    Decision = "allowed"
	DecisionConfirm    Decision = "confirm"
	DecisionApproved   Decision = "approved"
	DecisionDenied     Decision = "denied"
	DecisionExpired    Decision = "expired"
	DecisionForbidden  Decision = "forbidden"
	DecisionOutOfGrant Decision = "out_of_grant"
	DecisionPlanned    Decision = "planned" // a command an approved plan lists (ADR-0040)
)

// Entry is one journal row.
type Entry struct {
	Key, TurnID string
	N           int
	Tool        string
	Principal   string
	Args        string // redacted, at most 2 KiB
	ResultHead  string // redacted, the first 512 runes
	ResultLen   int    // runes of the whole result
	Err         string
	Ms          int64
	Decision    Decision
	Tainted     bool
	At          time.Time
}

// Journal is the tool_calls table.
type Journal struct {
	db     *sql.DB
	now    func() time.Time
	retain time.Duration
	log    *slog.Logger
	known  func(name string) bool
	in     instruments
}

// Option configures a Journal.
type Option func(*Journal)

// WithLogger sets where a failed write is reported. The default drops it.
func WithLogger(l *slog.Logger) Option { return func(j *Journal) { j.log = l } }

// WithTools sets which tool names are real, the registry's: only they
// label the metrics. A name the model made up is counted as "unknown",
// and so is every name when this is not set.
func WithTools(known func(name string) bool) Option { return func(j *Journal) { j.known = known } }

// New migrates the "tool_journal" component. Entries older than retain are
// removed by Prune; retain <= 0 keeps them forever (Prune does nothing).
// DefaultRetain is the usual value, passed by the composition root.
func New(ctx context.Context, db *sql.DB, now func() time.Time, retain time.Duration, opts ...Option) (*Journal, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "tool_journal", sub); err != nil {
		return nil, err
	}
	j := &Journal{db: db, now: now, retain: retain, log: slog.New(slog.DiscardHandler), known: func(string) bool { return false }, in: newInstruments()}
	for _, o := range opts {
		o(j)
	}
	return j, nil
}

type holderKey struct{}

// holder carries the decision from an inner middleware to the journal. A
// call runs on one goroutine, so it needs no lock.
type holder struct{ decision Decision }

// Annotate sets the decision of the call in ctx. The policy calls it; with
// no journal around the call it does nothing.
func Annotate(ctx context.Context, d Decision) {
	if h, ok := ctx.Value(holderKey{}).(*holder); ok {
		h.decision = d
	}
}

// Middleware records every call that passes through it. The decision is
// DecisionAllowed unless an inner middleware sets another with Annotate.
// A failed write is logged and does not fail the call: its effects are
// done by then.
func (j *Journal) Middleware() tool.Middleware {
	return func(next tool.Func) tool.Func {
		return func(ctx context.Context, c tool.Call) (tool.Result, error) {
			h := &holder{decision: DecisionAllowed}
			started := j.now()
			res, err := next(context.WithValue(ctx, holderKey{}, h), c)
			ended := j.now()
			j.record(ctx, entryOf(c, res, err, h.decision, started, ended))
			j.observe(ctx, c.Name, h.decision, err, ended.Sub(started))
			return res, err
		}
	}
}

// entryOf is what the journal keeps of a finished call.
func entryOf(c tool.Call, res tool.Result, err error, d Decision, started, ended time.Time) Entry {
	e := Entry{
		Key: c.Key(), TurnID: c.TurnID, N: c.N, Tool: c.Name, Principal: c.Principal,
		Args:       capBytes(redact.String(string(c.Args)), maxArgsBytes),
		ResultHead: capRunes(redact.String(res.Text), maxHeadRunes),
		ResultLen:  utf8.RuneCountInString(res.Text),
		Ms:         ended.Sub(started).Milliseconds(), Decision: d, Tainted: c.Tainted, At: started,
	}
	if err != nil {
		e.Err = capRunes(redact.String(err.Error()), maxErrRunes)
	}
	return e
}

// capRunes keeps the first n runes of s.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// capBytes keeps at most n bytes of s, cut on a rune boundary.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// record stores e even when the turn's context is done: a call that ran
// is journalled.
func (j *Journal) record(ctx context.Context, e Entry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	_, err := j.db.ExecContext(ctx, `INSERT OR REPLACE INTO tool_calls
		(key, decision, turn_id, n, tool, principal, args, result_head, result_len, err, ms, tainted, at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Key, string(e.Decision), e.TurnID, e.N, e.Tool, e.Principal, e.Args, e.ResultHead, e.ResultLen, e.Err, e.Ms, e.Tainted, e.At.UnixMilli())
	if err != nil {
		j.log.Error("journal: call not recorded", "key", e.Key, "tool", e.Tool, "err", err)
	}
}

// Entries returns every entry, oldest first.
func (j *Journal) Entries(ctx context.Context) ([]Entry, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT key, decision, turn_id, n, tool, principal, args, result_head,
		result_len, err, ms, tainted, at_ms FROM tool_calls ORDER BY at_ms, rowid`)
	if err != nil {
		return nil, fmt.Errorf("journal: entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		var decision string
		var at int64
		if err := rows.Scan(&e.Key, &decision, &e.TurnID, &e.N, &e.Tool, &e.Principal, &e.Args, &e.ResultHead,
			&e.ResultLen, &e.Err, &e.Ms, &e.Tainted, &at); err != nil {
			return nil, fmt.Errorf("journal: entries: %w", err)
		}
		e.Decision, e.At = Decision(decision), time.UnixMilli(at).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than the retention and returns how many; with
// no retention it deletes nothing.
func (j *Journal) Prune(ctx context.Context) (int, error) {
	if j.retain <= 0 {
		return 0, nil
	}
	res, err := j.db.ExecContext(ctx, `DELETE FROM tool_calls WHERE at_ms < ?`, j.now().Add(-j.retain).UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("journal: prune: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Pruner prunes the journal at start and then every Every. It is a node
// component.
type Pruner struct {
	Journal *Journal
	Every   time.Duration
	Log     *slog.Logger
}

// Name implements node.Component.
func (Pruner) Name() string { return "prune-journal" }

// Run prunes until ctx ends, then returns nil. A failed prune is logged
// and tried again next time.
func (p Pruner) Run(ctx context.Context) error {
	for {
		if n, err := p.Journal.Prune(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.Log.Error("journal: prune failed", "err", err)
		} else if n > 0 {
			p.Log.Info("journal: pruned old tool calls", "rows", n)
		}
		timer := time.NewTimer(p.Every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
