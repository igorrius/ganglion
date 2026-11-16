package runner_test

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

type ctxKey struct{}

// R-RUN-20: OnCall is told each host call's kind, denied calls included,
// with the caller's context; log, now_ms and take are not reported.
func TestOnCallReportsEachHostCallsKind(t *testing.T) {
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		kinds = map[string]int{}
		seen  []any
	)
	deps := runner.Deps{OnCall: func(ctx context.Context, kind string) {
		mu.Lock()
		defer mu.Unlock()
		kinds[kind]++
		seen = append(seen, ctx.Value(ctxKey{}))
	}}
	m, err := runner.Load(context.Background(), probe, runner.Manifest{
		Name: "probe", Version: 1, Limits: runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second},
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	ctx := context.WithValue(context.Background(), ctxKey{}, "run-1")
	for _, op := range []string{
		`{"op":"fetch","url":"http://127.0.0.1:1/"}`, // denied by the manifest: still a call
		`{"op":"cell_get","name":"x.y"}`,
		`{"op":"cell_report","name":"soc","json":1}`,
		`{"op":"cell_report","name":"soc","json":2}`,
		`{"op":"notify","value":"hello"}`,
		`{"op":"state_get","name":"k"}`,
		`{"op":"state_put","name":"k","value":"v"}`,
		`{"op":"secret","name":"S"}`,
		`{"op":"config","name":"c"}`,
		`{"op":"now"}`,
		`{"op":"log","value":"x"}`,
	} {
		if _, err := m.Handle(ctx, pdk.Input{Trigger: "cron", Payload: json.RawMessage(op)}); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	want := map[string]int{
		runner.CallHTTPFetch: 1, runner.CallCellGet: 1, runner.CallCellReport: 2, runner.CallNotify: 1,
		runner.CallStateGet: 1, runner.CallStatePut: 1, runner.CallSecretGet: 1, runner.CallConfigGet: 1,
	}
	if !maps.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for _, v := range seen {
		if v != "run-1" {
			t.Fatalf("a call's context lost the caller's value: %v", v)
		}
	}
}
