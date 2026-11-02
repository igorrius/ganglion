// Package turns is the per-turn trace: route, tool calls, tokens, cost and
// outcome of every handled event (docs/spec.md, R-AGT-5). It also feeds the
// daily limits and the router's budget.
package turns

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Attempt is one model tried in a turn; Error is the error kind, empty on
// success.
type Attempt struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Error    string `json:"error,omitempty"`
}

// ToolUse is one tool call in a turn.
type ToolUse struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Ms   int64  `json:"ms"`
}

// Outcome is how a turn ended. Outcomes are stored in the turns table and
// label metrics, so their values never change.
type Outcome string

// Outcomes.
const (
	OutcomeOK         Outcome = "ok"
	OutcomeStepLimit  Outcome = "step_limit"
	OutcomeDailyLimit Outcome = "daily_limit"
	OutcomeNoModel    Outcome = "no_model"
	OutcomeCommand    Outcome = "command"
	OutcomeError      Outcome = "error"
)

// Turn is one row of the trace.
type Turn struct {
	EventID, Conv, Principal string
	Started, Finished        time.Time
	Steps                    int
	Route                    []Attempt
	Tools                    []ToolUse
	InputTokens              int // includes the cache counts
	CacheReadTokens          int
	CacheWriteTokens         int
	OutputTokens             int
	CostUSD                  float64
	Outcome                  Outcome
	Error                    string
}

// Totals sums turns.
type Totals struct {
	Turns   int // turns a model answered (ok, step_limit)
	Tokens  int // input + output of every call, compaction included
	CostUSD float64
}

// Log is the turns table.
type Log struct{ db *sql.DB }

// New migrates the "turns" component.
func New(ctx context.Context, db *sql.DB) (*Log, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "turns", sub); err != nil {
		return nil, err
	}
	return &Log{db: db}, nil
}

// Record stores t, replacing the row of a turn that ran before for the same
// event.
func (l *Log) Record(ctx context.Context, t Turn) error {
	route, err := json.Marshal(nonNil(t.Route))
	if err != nil {
		return err
	}
	tools, err := json.Marshal(nonNil(t.Tools))
	if err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, `INSERT OR REPLACE INTO turns
		(event_id, conv, principal, started_ms, finished_ms, steps, route, tools, input_tokens,
		 cache_read_tokens, cache_write_tokens, output_tokens, cost_usd, outcome, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.EventID, t.Conv, t.Principal, t.Started.UnixMilli(), t.Finished.UnixMilli(), t.Steps,
		string(route), string(tools), t.InputTokens, t.CacheReadTokens, t.CacheWriteTokens,
		t.OutputTokens, t.CostUSD, t.Outcome, t.Error)
	if err != nil {
		return fmt.Errorf("turns: record %s: %w", t.EventID, err)
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Get returns the trace of one event.
func (l *Log) Get(ctx context.Context, eventID string) (Turn, error) {
	var (
		t                 Turn
		started, finished int64
		route, tools      string
	)
	err := l.db.QueryRowContext(ctx, `SELECT event_id, conv, principal, started_ms, finished_ms, steps, route, tools,
		input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_usd, outcome, error
		FROM turns WHERE event_id = ?`, eventID).Scan(&t.EventID, &t.Conv, &t.Principal, &started, &finished, &t.Steps,
		&route, &tools, &t.InputTokens, &t.CacheReadTokens, &t.CacheWriteTokens, &t.OutputTokens, &t.CostUSD, &t.Outcome, &t.Error)
	if err != nil {
		return Turn{}, fmt.Errorf("turns: get %s: %w", eventID, err)
	}
	t.Started, t.Finished = time.UnixMilli(started).UTC(), time.UnixMilli(finished).UTC()
	if err := json.Unmarshal([]byte(route), &t.Route); err != nil {
		return Turn{}, err
	}
	if err := json.Unmarshal([]byte(tools), &t.Tools); err != nil {
		return Turn{}, err
	}
	return t, nil
}

// Totals sums the turns started at or after since. principal "" counts
// everyone; except leaves one event out, so a turn that reruns does not
// count its own earlier row. Refusals, apologies and commands spend no
// turn.
func (l *Log) Totals(ctx context.Context, since time.Time, principal, except string) (Totals, error) {
	var t Totals
	err := l.db.QueryRowContext(ctx, `SELECT
		COALESCE(sum(outcome IN ('ok', 'step_limit')), 0),
		COALESCE(sum(input_tokens + output_tokens), 0),
		COALESCE(sum(cost_usd), 0)
		FROM turns WHERE started_ms >= ? AND (? = '' OR principal = ?) AND event_id != ?`,
		since.UnixMilli(), principal, principal, except).Scan(&t.Turns, &t.Tokens, &t.CostUSD)
	if err != nil {
		return Totals{}, fmt.Errorf("turns: totals: %w", err)
	}
	return t, nil
}

// SpentSince implements the router's Budget.
func (l *Log) SpentSince(ctx context.Context, t time.Time) (float64, error) {
	tot, err := l.Totals(ctx, t, "", "")
	return tot.CostUSD, err
}
