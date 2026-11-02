package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/turns"
)

func TestRunStagesStopsAtTheStageThatEndsTheTurn(t *testing.T) {
	var ran []string
	mk := func(name string, done bool) stage {
		return func(context.Context, *turn) (bool, error) { ran = append(ran, name); return done, nil }
	}
	if err := runStages(context.Background(), &turn{}, []stage{mk("a", false), mk("b", true), mk("c", false)}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"a", "b"}) {
		t.Fatalf("ran %v, want a, b", ran)
	}
}

func TestRunStagesStopsAtAnError(t *testing.T) {
	boom := errors.New("boom")
	called := false
	err := runStages(context.Background(), &turn{}, []stage{
		func(context.Context, *turn) (bool, error) { return false, boom },
		func(context.Context, *turn) (bool, error) { called = true; return false, nil },
	})
	if !errors.Is(err, boom) || called {
		t.Fatalf("err %v, later stage called %v", err, called)
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	want := Config{MaxSteps: 8, TurnTimeout: 2 * time.Minute, CallTimeout: 60 * time.Second, HistoryTokens: 6000,
		KeepRecent: 12, DailyTurns: 300, DailyTokens: 1_000_000, SessionIdle: 6 * time.Hour, Location: time.UTC}
	if c != want {
		t.Fatalf("DefaultConfig() = %+v\nwant %+v", c, want)
	}
}

// Zero daily limits are off: no -1 sentinel (conventions, section 2).
func TestZeroDailyLimitsAreOff(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	cfg := DefaultConfig()
	cfg.DailyTurns, cfg.DailyTokens = 0, 0
	e.a = New(cfg, e.deps)
	ctx := context.Background()
	for i := range 301 {
		if err := e.turns.Record(ctx, turns.Turn{EventID: fmt.Sprintf("pre%d", i), Principal: "telegram:user:42",
			Started: t0, Finished: t0, Outcome: turns.OutcomeOK, InputTokens: 10_000}); err != nil {
			t.Fatal(err)
		}
	}
	if out := e.turn(t, message("telegram:user:42", "Igor", "hi")); out[0].Text != "ok" {
		t.Fatalf("with limits off: %+v", out)
	}
}

func TestSessionCommandsAnswerWithoutAModel(t *testing.T) {
	for _, text := range []string{"/new", "/reset", "/sessions", "/resume 1", "/NEW@ganglion_bot"} {
		e := newEnv(t, Config{}, nil)
		ev := message("telegram:user:42", "Igor", text)
		e.turn(t, ev)
		tr, err := e.turns.Get(context.Background(), ev.ID)
		if err != nil || tr.Outcome != turns.OutcomeCommand || len(e.smart.requests()) != 0 {
			t.Errorf("%s: outcome %q, %d model calls, %v", text, tr.Outcome, len(e.smart.requests()), err)
		}
	}
	e := newEnv(t, Config{}, nil)
	e.turn(t, message("telegram:user:42", "Igor", "/weather"))
	if len(e.smart.requests()) != 1 {
		t.Fatal("an unknown command did not reach the model")
	}
}
