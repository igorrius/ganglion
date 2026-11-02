package turns_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/turns"
)

var t0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

func newLog(t *testing.T) *turns.Log {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := turns.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func turn(id, principal string, at time.Time, in, out int, cost float64) turns.Turn {
	return turns.Turn{
		EventID: id, Conv: "telegram:1", Principal: principal, Started: at, Finished: at.Add(2 * time.Second),
		Steps: 1, Route: []turns.Attempt{{Provider: "or", Model: "a", Error: "rate_limit"}, {Provider: "claude", Model: "haiku"}},
		Tools: []turns.ToolUse{{Name: "clock", OK: true, Ms: 3}}, InputTokens: in, CacheReadTokens: in / 2, OutputTokens: out,
		CostUSD: cost, Outcome: "ok",
	}
}

func TestRecordAndTotals(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	for _, tr := range []turns.Turn{
		turn("e1", "telegram:user:1", t0, 100, 20, 0.01),
		turn("e2", "telegram:user:2", t0.Add(time.Minute), 200, 30, 0.02),
	} {
		if err := l.Record(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Totals(ctx, t0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns != 2 || got.Tokens != 350 || got.CostUSD < 0.0299 || got.CostUSD > 0.0301 {
		t.Fatalf("totals %+v", got)
	}
	back, err := l.Get(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	want := turn("e1", "telegram:user:1", t0, 100, 20, 0.01)
	if back.Route[0] != want.Route[0] || back.Tools[0] != want.Tools[0] || back.CacheReadTokens != 50 || !back.Started.Equal(t0) || back.Outcome != "ok" {
		t.Fatalf("Get = %+v", back)
	}
}

func TestRerunReplacesRow(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	_ = l.Record(ctx, turn("e1", "telegram:user:1", t0, 100, 20, 0.01))
	_ = l.Record(ctx, turn("e1", "telegram:user:1", t0, 300, 40, 0.03))
	got, _ := l.Totals(ctx, t0, "", "")
	if got.Turns != 1 || got.Tokens != 340 {
		t.Fatalf("after a rerun %+v", got)
	}
}

func TestTotalsPerPrincipal(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	_ = l.Record(ctx, turn("e1", "telegram:user:1", t0, 100, 20, 0.01))
	_ = l.Record(ctx, turn("e2", "telegram:user:2", t0, 200, 30, 0.02))
	got, _ := l.Totals(ctx, t0, "telegram:user:2", "")
	if got.Turns != 1 || got.Tokens != 230 {
		t.Fatalf("per principal %+v", got)
	}
}

func TestSpentSince(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	_ = l.Record(ctx, turn("old", "telegram:user:1", t0.Add(-25*time.Hour), 100, 20, 0.5))
	_ = l.Record(ctx, turn("new", "telegram:user:1", t0, 100, 20, 0.25))
	usd, err := l.SpentSince(ctx, t0.Add(-time.Hour))
	if err != nil || usd != 0.25 {
		t.Fatalf("SpentSince = %v, %v", usd, err)
	}
}

func TestTotalsCountOnlyModelTurns(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	for i, outcome := range []turns.Outcome{turns.OutcomeOK, turns.OutcomeStepLimit, turns.OutcomeDailyLimit, turns.OutcomeNoModel, turns.OutcomeCommand} {
		tr := turn(fmt.Sprintf("e%d", i), "telegram:user:1", t0, 10, 1, 0)
		tr.Outcome = outcome
		_ = l.Record(ctx, tr)
	}
	got, _ := l.Totals(ctx, t0, "telegram:user:1", "")
	if got.Turns != 2 || got.Tokens != 55 {
		t.Fatalf("totals %+v: turns count only answered turns, tokens count every call", got)
	}
}

func TestTotalsExceptEvent(t *testing.T) {
	l := newLog(t)
	ctx := context.Background()
	_ = l.Record(ctx, turn("e1", "telegram:user:1", t0, 100, 20, 0.01))
	_ = l.Record(ctx, turn("e2", "telegram:user:1", t0, 100, 20, 0.01))
	if got, _ := l.Totals(ctx, t0, "telegram:user:1", "e2"); got.Turns != 1 || got.Tokens != 120 {
		t.Fatalf("totals except e2 %+v", got)
	}
}
