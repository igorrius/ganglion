package turns

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

const (
	// modelsSeen is how far back /status looks for each model's calls.
	modelsSeen = 7 * 24 * time.Hour
	// failedSeen is how far back it counts failed turns.
	failedSeen = 24 * time.Hour
)

// ModelSeen is a model's last answer and last failure in the turns read.
type ModelSeen struct {
	Model            string
	Answered, Failed time.Time // zero when none
	Kind             string    // the last failure's error kind, a closed set
}

// failing reports whether the model's last call failed.
func (m ModelSeen) failing() bool { return !m.Failed.IsZero() && m.Failed.After(m.Answered) }

// Name implements status.Source.
func (*Log) Name() string { return "turns" }

// Status implements status.Source (R-OBS-11): each model's last answer and
// failure in the last week, and the turns that failed in the last day. A
// model warns when its last call failed; every model so is a failure.
func (l *Log) Status(ctx context.Context, s status.Scope) []status.Check {
	seen, err := l.ModelsSeen(ctx, s.Now.Add(-modelsSeen))
	if err != nil {
		return []status.Check{noAnswer(status.SectionModels)}
	}
	out := modelLines(seen, s)
	n, err := l.failedSince(ctx, s.Now.Add(-failedSeen))
	if err != nil {
		return append(out, noAnswer(status.SectionProblems))
	}
	c := status.Check{Section: status.SectionProblems, State: status.OK, Line: texts.StatusTurnsFailed, Vars: texts.Vars{"n": strconv.Itoa(n)}}
	if n > 0 {
		c.State = status.Warn
	}
	return append(out, c)
}

// modelLines are the models' lines, in the order seen.
func modelLines(seen []ModelSeen, s status.Scope) []status.Check {
	all := len(seen) > 0
	for _, m := range seen {
		all = all && m.failing()
	}
	out := make([]status.Check, 0, len(seen))
	for _, m := range seen {
		c := status.Check{Section: status.SectionModels, State: status.OK, Line: texts.StatusModelAnswered,
			Vars: texts.Vars{"model": m.Model}}
		if m.Answered.IsZero() {
			c.Line = texts.StatusModelSilent
		} else {
			c.Vars["time"] = status.Clock(m.Answered, s)
		}
		if m.failing() {
			c.State, c.Reason, c.Since, c.Vars["kind"] = status.Warn, texts.StatusReasonModelFailed, m.Failed, m.Kind
			if all {
				c.State = status.Fail
			}
		}
		out = append(out, c)
	}
	return out
}

func noAnswer(sec status.Section) status.Check {
	return status.Check{Section: sec, State: status.Warn, Line: texts.StatusNoAnswer, Vars: texts.Vars{"component": "turns"}}
}

// ModelsSeen returns each model's last answer and last failure in the turns
// started at or after since, ordered by model. A run ended for an addition
// is neither.
func (l *Log) ModelsSeen(ctx context.Context, since time.Time) ([]ModelSeen, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT COALESCE(json_extract(a.value, '$.model'), ''),
		COALESCE(json_extract(a.value, '$.error'), ''), t.started_ms
		FROM turns t, json_each(t.route) a WHERE t.started_ms >= ?
		ORDER BY t.started_ms, t.event_id, a.key`, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("turns: models seen: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byModel, order := map[string]*ModelSeen{}, []string{}
	for rows.Next() {
		var model, kind string
		var at int64
		if err := rows.Scan(&model, &kind, &at); err != nil {
			return nil, fmt.Errorf("turns: models seen: %w", err)
		}
		m, ok := byModel[model]
		if !ok {
			m = &ModelSeen{Model: model}
			byModel[model], order = m, append(order, model)
		}
		switch kind {
		case "":
			m.Answered = time.UnixMilli(at).UTC()
		case EndedForAddition:
		default:
			m.Failed, m.Kind = time.UnixMilli(at).UTC(), kind
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("turns: models seen: %w", err)
	}
	slices.Sort(order)
	out := make([]ModelSeen, len(order))
	for i, name := range order {
		out[i] = *byModel[name]
	}
	return out, nil
}

// failedSince counts the turns started at or after since that ended in an
// error or with no model.
func (l *Log) failedSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE started_ms >= ? AND outcome IN (?, ?)`,
		since.UnixMilli(), OutcomeError, OutcomeNoModel).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("turns: failed turns: %w", err)
	}
	return n, nil
}
