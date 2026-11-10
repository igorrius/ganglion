package modhost_test

import (
	"context"
	"testing"
	"time"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// Runs are counted by module, version, trigger and result; a crash loop's
// disable and the escalations, held ones included, have their own counters
// (design section 8.6).
func TestModuleMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t, active(t, "local/probe", probeManifest))
	e.start(t)
	if err := e.host.RunCron(context.Background(), "local/probe", "slot"); err != nil {
		t.Fatal(err)
	}
	for range 2 { // counted, never escalated: the calling turn has the words
		e.call(t, "probe.broken") //nolint:errcheck // the failure is the point
		e.now = e.now.Add(time.Minute)
	}
	for range 3 {
		e.cron(t, cronBreaks)
		e.now = e.now.Add(time.Minute)
	}
	for labels, want := range map[[3]string]float64{
		{"cron", "ok", "ganglion.modhost.runs"}:     1,
		{"tool", "format", "ganglion.modhost.runs"}: 2,
		{"cron", "format", "ganglion.modhost.runs"}: 3,
	} {
		got := rec.Value(t, labels[2], map[string]string{"module": "local/probe", "version": "3", "trigger": labels[0], "result": labels[1]})
		if got != want {
			t.Errorf("%v = %v, want %v", labels, got, want)
		}
	}
	if got := rec.Value(t, "ganglion.modhost.disables", map[string]string{"module": "local/probe", "reason": "crashloop"}); got != 1 {
		t.Errorf("disables = %v", got)
	}
	for outcome, want := range map[string]float64{"put": 1, "held": 2} {
		got := rec.Value(t, "ganglion.modhost.escalations", map[string]string{"module": "local/probe", "kind": "module.format", "outcome": outcome})
		if got != want {
			t.Errorf("format escalations %s = %v, want %v", outcome, got, want)
		}
	}
}
