package schedule_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// Every run the scheduler puts is counted by kind and result, and a
// scheduled run's lag behind its slot is a histogram: a run whose put
// failed is put on the next tick, later.
func TestRunMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	e := newEnv(t)
	e.create(t, task("j1", "0 * * * *"))
	e.create(t, reminder("r1", t0.Add(30*time.Minute)))
	e.now = t0.Add(time.Hour) // 30 s past j1's slot, 30 min past r1's
	e.q.fail = errors.New("disk full")
	_ = e.sched.Tick(context.Background())
	e.now = e.now.Add(time.Minute)
	e.tick(t)
	if err := e.sched.RunNow(context.Background(), "j1", owner); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		attrs map[string]string
		want  float64
	}{
		{map[string]string{"kind": "task", "result": "ok"}, 2},
		{map[string]string{"kind": "reminder", "result": "ok"}, 1},
		{map[string]string{"result": "error"}, 1},
	} {
		if got := rec.Value(t, "ganglion.schedule.runs", c.attrs); got != c.want {
			t.Errorf("runs %v = %v, want %v", c.attrs, got, c.want)
		}
	}
	if got := rec.Value(t, "ganglion.schedule.lag", nil); got != 2 {
		t.Errorf("%v lags recorded, want the two scheduled runs only", got)
	}
	if got := rec.Sum(t, "ganglion.schedule.lag", map[string]string{"kind": "task"}) + rec.Sum(t, "ganglion.schedule.lag", map[string]string{"kind": "reminder"}); got != 30+1800+60 {
		t.Errorf("lags sum to %v s, want %v", got, 30+1800+60)
	}
}
