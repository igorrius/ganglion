package diag

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/turns"
)

// callRow is one tool_calls row; its arguments and result were redacted
// when they were journalled and are redacted again here.
type callRow struct {
	Key       string `json:"key"`
	Decision  string `json:"decision"`
	Turn      string `json:"turn"`
	N         int    `json:"n"`
	Tool      string `json:"tool"`
	Principal string `json:"principal,omitempty"`
	Args      string `json:"args,omitempty"`
	Result    string `json:"result_head,omitempty"`
	ResultLen int    `json:"result_len,omitempty"`
	Err       string `json:"err,omitempty"`
	Ms        int64  `json:"ms"`
	Tainted   bool   `json:"tainted,omitempty"`
	At        string `json:"at"`
}

const callColumns = `key, decision, turn_id, n, tool, principal, args, result_head, result_len, err, ms, tainted, at_ms`

func scanCalls(rows *sql.Rows) ([]callRow, error) {
	defer func() { _ = rows.Close() }()
	var out []callRow
	for rows.Next() {
		var (
			c  callRow
			at int64
		)
		if err := rows.Scan(&c.Key, &c.Decision, &c.Turn, &c.N, &c.Tool, &c.Principal, &c.Args, &c.Result,
			&c.ResultLen, &c.Err, &c.Ms, &c.Tainted, &at); err != nil {
			return nil, err
		}
		c.Args, c.Result, c.Err, c.At = redact.String(c.Args), redact.String(c.Result), redact.String(c.Err), stamp(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

func stamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

// missing turns "no such table" into a plain answer: the component that
// owns the table has never run on this database.
func missing(err error) error {
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("diag: %w (the component has not run on this database)", err)
	}
	return err
}

func (d *Diag) journalSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		window
		Turn     string `json:"turn"`
		Tool     string `json:"tool"`
		Decision string `json:"decision"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	since, limit, err := d.bounds(a.window)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + callColumns + ` FROM tool_calls WHERE at_ms >= ?`
	args := []any{since.UnixMilli()}
	if a.Turn != "" { // a turn is searched whole, whatever the window: its id bounds the rows
		q, args = `SELECT `+callColumns+` FROM tool_calls WHERE 1 = 1`, nil
	}
	for col, v := range map[string]string{"turn_id": a.Turn, "tool": a.Tool, "decision": a.Decision} {
		if v != "" {
			q += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	rows, err := d.db.QueryContext(ctx, q+` ORDER BY at_ms DESC, n DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, missing(err)
	}
	calls, err := scanCalls(rows)
	return map[string]any{"calls": calls}, err
}

// turnRow is one turns row.
type turnRow struct {
	Turn      string          `json:"turn"`
	Conv      string          `json:"conv"`
	Principal string          `json:"principal,omitempty"`
	Started   string          `json:"started"`
	Ms        int64           `json:"ms"`
	Steps     int             `json:"steps"`
	Route     json.RawMessage `json:"route"`
	Tools     json.RawMessage `json:"tools"`
	Outcome   string          `json:"outcome"`
	Error     string          `json:"error,omitempty"`
	CostUSD   float64         `json:"cost_usd"`
}

const turnColumns = `event_id, conv, principal, started_ms, finished_ms, steps, route, tools, outcome, error, cost_usd`

func scanTurn(sc interface{ Scan(...any) error }) (turnRow, error) {
	var (
		t                 turnRow
		started, finished int64
		route, tools      string
	)
	if err := sc.Scan(&t.Turn, &t.Conv, &t.Principal, &started, &finished, &t.Steps, &route, &tools, &t.Outcome,
		&t.Error, &t.CostUSD); err != nil {
		return turnRow{}, err
	}
	t.Started, t.Ms, t.Error = stamp(started), finished-started, redact.String(t.Error)
	t.Route, t.Tools = jsonOr(redact.String(route)), jsonOr(tools)
	return t, nil
}

// jsonOr keeps s as JSON when it is, and as a JSON string otherwise.
func jsonOr(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	raw, _ := json.Marshal(s)
	return raw
}

func (d *Diag) turnsFailed(ctx context.Context, raw json.RawMessage) (any, error) {
	var w window
	if err := decode(raw, &w); err != nil {
		return nil, err
	}
	since, limit, err := d.bounds(w)
	if err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns
		WHERE outcome NOT IN (?, ?, ?) AND started_ms >= ? ORDER BY started_ms DESC LIMIT ?`,
		string(turns.OutcomeOK), string(turns.OutcomeCommand), string(turns.OutcomeReview), since.UnixMilli(), limit)
	if err != nil {
		return nil, missing(err)
	}
	defer func() { _ = rows.Close() }()
	var out []turnRow
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return map[string]any{"turns": out}, rows.Err()
}

// reply is an outbox row without its body: the text is the person's.
type reply struct {
	Key       string `json:"key"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	PartsSent int    `json:"parts_sent,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Created   string `json:"created"`
	Done      string `json:"done,omitempty"`
}

const replyColumns = `idem_key, status, attempts, parts_sent, last_error, created_ms, done_ms`

func scanReplies(rows *sql.Rows) ([]reply, error) {
	defer func() { _ = rows.Close() }()
	var out []reply
	for rows.Next() {
		var (
			r             reply
			created, done int64
		)
		if err := rows.Scan(&r.Key, &r.Status, &r.Attempts, &r.PartsSent, &r.LastError, &created, &done); err != nil {
			return nil, err
		}
		r.LastError, r.Created, r.Done = redact.String(r.LastError), stamp(created), stamp(done)
		out = append(out, r)
	}
	return out, rows.Err()
}

// likePrefix is a LIKE pattern for keys that start with p and a colon.
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p) + `:%`
}

func (d *Diag) turnShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Turn string `json:"turn"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Turn == "" {
		return nil, errors.New("diag: turn.show needs a turn id")
	}
	out := map[string]any{"turn_id": a.Turn}
	var (
		kind, source, conv, status, idem string
		at                               int64
		attempts                         int
	)
	err := d.db.QueryRowContext(ctx, `SELECT kind, source, conv, at_ms, status, attempts, idem_key FROM inbox WHERE id = ?`, a.Turn).
		Scan(&kind, &source, &conv, &at, &status, &attempts, &idem)
	switch {
	case err == nil:
		out["event"] = map[string]any{"kind": kind, "source": source, "conv": conv, "at": stamp(at), "status": status, "attempts": attempts}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, missing(err)
	}
	t, err := scanTurn(d.db.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE event_id = ?`, a.Turn))
	switch {
	case err == nil:
		out["turn"] = t
	case !errors.Is(err, sql.ErrNoRows):
		return nil, missing(err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+callColumns+` FROM tool_calls WHERE turn_id = ? ORDER BY n, at_ms`, a.Turn)
	if err != nil {
		return nil, missing(err)
	}
	if out["calls"], err = scanCalls(rows); err != nil {
		return nil, err
	}
	// The answer is keyed by the incoming event's idempotency key
	// ("<idem>:reply:0"); messages a call sent, by the call's key
	// ("<turn>:<n>:…").
	if idem == "" {
		idem = a.Turn // no inbox row: match nothing extra
	}
	rows, err = d.db.QueryContext(ctx, `SELECT `+replyColumns+` FROM outbox
		WHERE idem_key LIKE ? ESCAPE '\' OR idem_key LIKE ? ESCAPE '\' ORDER BY seq LIMIT ?`,
		likePrefix(a.Turn), likePrefix(idem), MaxLimit)
	if err != nil {
		return nil, missing(err)
	}
	out["replies"], err = scanReplies(rows)
	return out, err
}

func (d *Diag) queuesState(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := decode(raw, &struct{}{}); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, q := range []struct{ name, table, at string }{{"inbox", "inbox", "at_ms"}, {"outbox", "outbox", "created_ms"}} {
		counts, oldest, err := d.counts(ctx, q.table, q.at)
		if err != nil {
			return nil, missing(err)
		}
		out[q.name] = map[string]any{"by_status": counts, "oldest_pending": oldest}
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+replyColumns+` FROM outbox WHERE status = 'failed' ORDER BY seq DESC LIMIT 5`)
	if err != nil {
		return nil, missing(err)
	}
	if out["outbox_failed_latest"], err = scanReplies(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// counts are a queue table's rows by status and the time of its oldest
// pending row ("" when none).
func (d *Diag) counts(ctx context.Context, table, at string) (map[string]int, string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM `+table+` GROUP BY status`) // table and at are constants
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var (
			s string
			n int
		)
		if err := rows.Scan(&s, &n); err != nil {
			return nil, "", err
		}
		counts[s] = n
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var oldest sql.NullInt64
	if err := d.db.QueryRowContext(ctx, `SELECT MIN(`+at+`) FROM `+table+` WHERE status = 'pending'`).Scan(&oldest); err != nil {
		return nil, "", err
	}
	return counts, stamp(oldest.Int64), nil
}
