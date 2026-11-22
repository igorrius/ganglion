package modstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

// RunsKept is how many runs of a module the journal keeps (R-RUN-20).
const RunsKept = 200

// nodeNameRE is a cell id as runner's cell id pattern allows it, or a
// module's short name: lower-case letters, digits, "_" and "-", dotted.
var nodeNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$`)

// maxPlainName caps a shown name's bytes.
const maxPlainName = 253

// PlainName reports whether s is a cell id or a module's short name in the
// node's name pattern. A cell id or a module name comes from a manifest, so
// only a plain one is shown to the owner or the model (R-RUN-13, R-RUN-20);
// any other is shown as "?".
func PlainName(s string) bool { return len(s) <= maxPlainName && nodeNameRE.MatchString(s) }

// Hop is one run as a chain shows it: no ids beyond the module's name.
type Hop struct {
	Module  string         `json:"module"` // full name, "local/demo-reader"
	Version string         `json:"version"`
	Trigger string         `json:"trigger"`         // cron | tool | cell | event
	Tool    string         `json:"tool,omitempty"`  // the tool's name, "demo-reader.status", for tool
	Cells   []string       `json:"cells,omitempty"` // the cells whose change started it, for cell
	At      time.Time      `json:"at"`              // when the run started
	Took    time.Duration  `json:"took"`            // how long it ran
	Result  string         `json:"result"`          // modhost.Result
	Kind    string         `json:"kind,omitempty"`  // modhost.Kind when it failed
	Calls   map[string]int `json:"calls,omitempty"` // host call kind -> count
	Wrote   []string       `json:"wrote,omitempty"` // cell ids reported, sorted
	Sent    int            `json:"sent,omitempty"`  // messages notify put
}

// Run is one row of the journal: the run, its id, who produced it and the
// chain of runs that led to it, oldest first (at most 4).
type Run struct {
	Hop
	ID          string // the run's id: the cron slot, the call's key, the trigger event's id
	SHA256      string
	Producer    string // for cell: the module whose run wrote the cells
	ProducerRun string // and that run's id
	Depth       int
	Chain       []Hop
}

// RecordRun writes r, replacing a row with the same module and id, and
// prunes the module's rows beyond RunsKept (oldest by At, then rowid).
func (s *Store) RecordRun(ctx context.Context, r Run) error {
	cells, calls, wrote, chain, err := encodeRun(r)
	if err != nil {
		return err
	}
	err = sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `INSERT OR REPLACE INTO module_runs
			(module, run_id, version, sha256, trigger, tool, cells, producer, producer_run, depth,
			 at_ms, took_ms, result, kind, calls, wrote, sent, chain)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Module, r.ID, r.Version, r.SHA256, r.Trigger, r.Tool, cells, r.Producer, r.ProducerRun, r.Depth,
			r.At.UnixMilli(), r.Took.Milliseconds(), r.Result, r.Kind, calls, wrote, r.Sent, chain); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `DELETE FROM module_runs WHERE module = ? AND rowid NOT IN
			(SELECT rowid FROM module_runs WHERE module = ? ORDER BY at_ms DESC, rowid DESC LIMIT ?)`,
			r.Module, r.Module, RunsKept)
		return err
	})
	if err != nil {
		return fmt.Errorf("modstore: record run: %w", err)
	}
	return nil
}

// encodeRun is the JSON columns of r.
func encodeRun(r Run) (cells, calls, wrote, chain string, err error) {
	cs, err := json.Marshal(nonNil(r.Cells))
	if err != nil {
		return "", "", "", "", fmt.Errorf("modstore: record run: %w", err)
	}
	ca, err := json.Marshal(nonNilMap(r.Calls))
	if err != nil {
		return "", "", "", "", fmt.Errorf("modstore: record run: %w", err)
	}
	w, err := json.Marshal(nonNil(r.Wrote))
	if err != nil {
		return "", "", "", "", fmt.Errorf("modstore: record run: %w", err)
	}
	ch := r.Chain
	if ch == nil {
		ch = []Hop{}
	}
	h, err := json.Marshal(ch)
	if err != nil {
		return "", "", "", "", fmt.Errorf("modstore: record run: %w", err)
	}
	return string(cs), string(ca), string(w), string(h), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

// runColumns are the columns Run and Runs read, in scan order.
const runColumns = `module, run_id, version, sha256, trigger, tool, cells, producer, producer_run, depth,
	at_ms, took_ms, result, kind, calls, wrote, sent, chain`

// scanner is a row or rows.
type scanner interface{ Scan(dest ...any) error }

// scanRun decodes one journal row.
func scanRun(sc scanner) (Run, error) {
	var (
		r                          Run
		cells, calls, wrote, chain string
		atMS, tookMS               int64
	)
	if err := sc.Scan(&r.Module, &r.ID, &r.Version, &r.SHA256, &r.Trigger, &r.Tool, &cells, &r.Producer,
		&r.ProducerRun, &r.Depth, &atMS, &tookMS, &r.Result, &r.Kind, &calls, &wrote, &r.Sent, &chain); err != nil {
		return Run{}, err
	}
	r.At, r.Took = time.UnixMilli(atMS).UTC(), time.Duration(tookMS)*time.Millisecond
	for _, d := range []struct {
		raw string
		to  any
	}{{cells, &r.Cells}, {calls, &r.Calls}, {wrote, &r.Wrote}, {chain, &r.Chain}} {
		if err := json.Unmarshal([]byte(d.raw), d.to); err != nil {
			return Run{}, fmt.Errorf("decode: %w", err)
		}
	}
	if len(r.Cells) == 0 {
		r.Cells = nil
	}
	if len(r.Calls) == 0 {
		r.Calls = nil
	}
	if len(r.Wrote) == 0 {
		r.Wrote = nil
	}
	if len(r.Chain) == 0 {
		r.Chain = nil
	}
	return r, nil
}

// Run is the row of module's run id; false when the journal has none.
func (s *Store) Run(ctx context.Context, module, id string) (Run, bool, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM module_runs WHERE module = ? AND run_id = ?`, module, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Run{}, false, nil
	case err != nil:
		return Run{}, false, fmt.Errorf("modstore: run: %w", err)
	}
	return r, true, nil
}

// Runs are module's last n runs, newest first.
func (s *Store) Runs(ctx context.Context, module string, n int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM module_runs WHERE module = ?
		ORDER BY at_ms DESC, rowid DESC LIMIT ?`, module, n)
	if err != nil {
		return nil, fmt.Errorf("modstore: runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("modstore: runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("modstore: runs: %w", err)
	}
	return out, nil
}
