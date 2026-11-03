package runner_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// Every call of a module is counted by its result and timed: a module
// that reports a failure, a timeout and a trap are told apart.
func TestModuleCallMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := runner.Load(ctx, probe, runner.Manifest{Name: "probe", Version: 1,
		Limits: runner.Limits{Memory: 32 << 20, Timeout: 300 * time.Millisecond}}, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })
	for _, payload := range []string{`{"op":"echo"}`, `{"op":"fail","value":"x"}`, `{"op":"spin"}`, `{"op":"alloc","mib":64}`} {
		_, _ = m.Handle(ctx, pdk.Input{Trigger: "cron", Payload: json.RawMessage(payload)})
	}
	for _, result := range []string{"ok", "failed", "timeout", "trap"} {
		if got := rec.Value(t, "ganglion.module.calls", map[string]string{"module": "probe", "result": result}); got != 1 {
			t.Errorf("module calls with result %s = %v, want 1", result, got)
		}
	}
	if got := rec.Value(t, "ganglion.module.call.duration", map[string]string{"module": "probe"}); got != 4 {
		t.Errorf("module timed %v times, want 4", got)
	}
}
