package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
)

type nopInbox struct{}

func (nopInbox) Put(context.Context, event.Event) error { return nil }

// TestLiveClaudeToolsThroughBridge runs a real `claude -p` with Haiku
// over the MCP bridge (ADR-0036). It needs the claude CLI logged
// in and a built ganglion binary for the relay:
//
//	go build -o /tmp/ganglion . && GANGLION_LIVE_CLAUDE=1 GANGLION_RELAY=/tmp/ganglion go test -run LiveClaude -v ./agent/
func TestLiveClaudeToolsThroughBridge(t *testing.T) {
	relay := os.Getenv("GANGLION_RELAY")
	if os.Getenv("GANGLION_LIVE_CLAUDE") == "" || relay == "" {
		t.Skip("set GANGLION_LIVE_CLAUDE=1 and GANGLION_RELAY=<ganglion binary> to call the real claude")
	}
	ctx := context.Background()
	base := newEnv(t, Config{}, nil) // for its database and pending store
	st, err := schedule.NewStore(ctx, base.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, schedule.Job{ID: "01JOB", Owner: owner, Conv: "telegram:42", Transport: "telegram",
		Kind: schedule.KindReminder, Cron: "0 8 * * *", Text: "water the plants", Created: t0}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return t0 }
	list := builtin.NewScheduleList(builtin.Scheduling{Jobs: st, Runner: schedule.New(st, nopInbox{}, time.UTC, now),
		Specs: func(string) (tool.Spec, bool) { return tool.Spec{}, false }, Buttons: base.pending,
		Convs: builtin.Convs{States: convStates{}, Listed: func(context.Context, string) (bool, error) { return false, nil },
			Private: func(string) (string, string, bool) { return "", "", false },
			Names:   func(context.Context, string) (string, error) { return "", nil }},
		Loc: time.UTC, Now: now})
	e := newConfirmEnvOn(t, base, list)
	e.a.cfg.CallTimeout, e.a.cfg.TurnTimeout = 2*time.Minute, 3*time.Minute
	r, err := rules.New([]router.Model{{Provider: "claude", Name: "haiku", Tier: router.TierCheap, Caps: router.ModelCaps{Tools: true}}})
	if err != nil {
		t.Fatal(err)
	}
	e.a.d.Router = r
	e.a.d.Providers = map[string]provider.Provider{"claude": claudecli.New("claude", claudecli.WithRelay(relay))}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", e.logs)
		}
	})
	ev := message(owner, "Igor", "Call the schedule_list tool once and tell me what jobs I have. Answer in one sentence.")
	out := e.turn(t, ev)
	t.Logf("answer: %q", reply(t, out))
	entries, err := e.journal.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		b, _ := json.Marshal(en)
		t.Logf("tool_calls row: %s", b)
	}
	if d := e.decisions(t, ev.ID+":1"); len(d) != 1 || d[0] != journal.DecisionAllowed {
		t.Fatalf("tool_calls for %s: %v", ev.ID+":1", d)
	}
	if _, ok := findOut(out, func(o event.Outgoing) bool { return o.IdemKey == ev.ID+":1:list:01JOB" }); !ok {
		t.Fatalf("no list entry with buttons in %+v", out)
	}
	rec, _ := e.turns.Get(context.Background(), ev.ID)
	t.Logf("route %+v, tools %+v", rec.Route, rec.Tools)
}
