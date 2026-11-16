package runner_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

// loadProbeWith loads the probe module with the given deps.
func loadProbeWith(t *testing.T, deps runner.Deps) *runner.Module {
	t.Helper()
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	m := runner.Manifest{
		Name:    "probe",
		Version: 1,
		Limits:  runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second},
	}
	mod, err := runner.Load(context.Background(), probe, m, deps)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = mod.Close(context.Background()) })
	return mod
}

func probeNow(t *testing.T, mod *runner.Module) int64 {
	t.Helper()
	out, err := mod.Handle(context.Background(), pdk.Input{Trigger: "cron", Payload: json.RawMessage(`{"op":"now"}`)})
	if err != nil || !out.OK {
		t.Fatalf("Handle: %v %+v", err, out)
	}
	var r struct {
		Unix int64 `json:"unix"`
	}
	if err := json.Unmarshal(out.Result, &r); err != nil {
		t.Fatal(err)
	}
	return r.Unix
}

func TestClockInjected(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	mod := loadProbeWith(t, runner.Deps{Clock: func() time.Time { return at }})
	if got := probeNow(t, mod); got != at.Unix() {
		t.Fatalf("now = %d, want %d", got, at.Unix())
	}
}

func TestClockAdvances(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	now := at
	mod := loadProbeWith(t, runner.Deps{Clock: func() time.Time { return now }})
	first := probeNow(t, mod)
	now = at.Add(10 * time.Minute)
	second := probeNow(t, mod)
	if second-first != 600 {
		t.Fatalf("clock moved by %d s, want 600", second-first)
	}
}
