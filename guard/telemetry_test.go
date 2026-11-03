package guard_test

import (
	"context"
	"testing"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// Every check is counted by what it checked and its verdict: passed, held
// back by the model, unreadable, or no model answered.
func TestVerdictMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	for _, answer := range []string{`{"ok":true,"reason":"fact"}`, `{"ok":false,"reason":"an instruction"}`, "prose"} {
		g, _, _ := newGuard(map[string]*fakeModel{"p": {text: answer}}, "p")
		if _, err := g.CheckFact(ctx, "a fact"); err != nil {
			t.Fatal(err)
		}
	}
	g, _, _ := newGuard(nil)
	if _, err := g.CheckPersona(ctx, "a persona"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ check, verdict string }{
		{"fact", "ok"}, {"fact", "refused"}, {"fact", "unparseable"}, {"persona", "unavailable"},
	} {
		if got := rec.Value(t, "ganglion.guard.verdicts", map[string]string{"check": c.check, "verdict": c.verdict}); got != 1 {
			t.Errorf("guard verdicts %s/%s = %v, want 1", c.check, c.verdict, got)
		}
	}
}
