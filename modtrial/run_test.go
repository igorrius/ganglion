package modtrial

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

var (
	probeOnce sync.Once
	probeWasm []byte
	probeErr  error
)

func probe(t *testing.T) []byte {
	t.Helper()
	probeOnce.Do(func() { probeWasm, probeErr = runnertest.BuildProbe() })
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	return probeWasm
}

var start = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

func testManifest() runner.Manifest {
	str := runner.ConfigKey{Type: "string"}
	return runner.Manifest{
		Name:    "probe",
		Version: 1,
		Config:  map[string]runner.ConfigKey{"op_cron": str, "op_cell": str, "op_tool_status": str, "mode": {Type: "string", Default: "dflt"}},
		Triggers: []runner.Trigger{
			{Cell: []string{"other.level"}},
		},
		Capabilities: runner.Capabilities{
			HTTPFetch: []string{"api.example.com"},
			SecretGet: []string{"TOKEN"},
			State:     true,
			Notify:    &runner.Notify{PerDay: 5},
		},
		Cells: runner.Cells{
			Report: map[string]runner.CellDecl{"soc": {Kind: "sensor", Type: runner.Shape{Type: "number"}}},
			Read:   []string{"other.level"},
		},
		Limits: runner.Limits{Memory: 64 << 20, Timeout: 5 * time.Second},
	}
}

func opts() Options { return Options{Start: start} }

func mkEx(name string, steps ...Step) Example {
	return Example{Name: name, Steps: steps}
}

func cron(exp Expect) Step {
	return Step{Trigger: Trigger{Kind: "cron"}, Expect: exp}
}

func run(t *testing.T, m runner.Manifest, o Options, es ...Example) Report {
	t.Helper()
	rep, err := Run(context.Background(), probe(t), m, "probe", Examples{Examples: es}, o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func withOp(e Example, key, op string) Example {
	if e.Config == nil {
		e.Config = map[string]string{}
	}
	e.Config[key] = op
	return e
}

func TestNotifyCounted(t *testing.T) {
	e := withOp(mkEx("n", cron(Expect{Messages: new(1)})), "op_cron", `{"op":"notify","value":"hi"}`)
	rep := run(t, testManifest(), opts(), e)
	if !rep.Passed {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Examples[0].Steps[0].Messages; len(got) != 1 || got[0] != "hi" {
		t.Fatalf("messages = %v", got)
	}
}

func TestNotifyMismatch(t *testing.T) {
	e := withOp(mkEx("n", cron(Expect{Messages: new(0)})), "op_cron", `{"op":"notify","value":"hi"}`)
	rep := run(t, testManifest(), opts(), e)
	if rep.Passed || rep.Examples[0].Passed {
		t.Fatal("want a failed trial")
	}
	if got := rep.Examples[0].Steps[0].Mismatch; got != "expected 0 messages, got 1" {
		t.Fatalf("mismatch = %q", got)
	}
}

func TestNotifyPerDay(t *testing.T) {
	m := testManifest()
	m.Capabilities.Notify.PerDay = 1
	one := Expect{Messages: new(1)}
	zero := Expect{Messages: new(0)}
	same := withOp(mkEx("same", cron(one), cron(zero)), "op_cron", `{"op":"notify","value":"x"}`)
	next := withOp(mkEx("next", cron(one), withAfter(cron(one), 24*time.Hour)), "op_cron", `{"op":"notify","value":"x"}`)
	rep := run(t, m, opts(), same, next)
	if !rep.Passed {
		t.Fatalf("report = %+v", rep)
	}
}

func withAfter(s Step, d time.Duration) Step { s.After = d; return s }

func TestCellGetAndVirtualTime(t *testing.T) {
	e := mkEx("cells",
		Step{Cells: map[string]json.RawMessage{"other.level": json.RawMessage(`42`)}},
		Step{After: 10 * time.Minute, Trigger: Trigger{Kind: "cron"}},
	)
	e = withOp(e, "op_cron", `{"op":"cell_get","name":"other.level"}`)
	rep := run(t, testManifest(), opts(), e)
	var got struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		AtMs  int64           `json:"at_ms"`
	}
	if err := json.Unmarshal(rep.Examples[0].Steps[1].out, &got); err != nil {
		t.Fatalf("answer %s: %v", rep.Examples[0].Steps[1].out, err)
	}
	// The cell was set in step 1, at the start; step 2 is 10 minutes later.
	if !got.OK || string(got.Value) != "42" || got.AtMs != start.UnixMilli() {
		t.Fatalf("cell_get = %+v, want 42 at %d", got, start.UnixMilli())
	}
	n := withOp(mkEx("now", Step{After: 10 * time.Minute, Trigger: Trigger{Kind: "cron"}}), "op_cron", `{"op":"now"}`)
	rep = run(t, testManifest(), opts(), n)
	var now struct {
		Unix int64 `json:"unix"`
	}
	if err := json.Unmarshal(rep.Examples[0].Steps[0].out, &now); err != nil || now.Unix != start.Add(10*time.Minute).Unix() {
		t.Fatalf("now = %s (%v), want %d", rep.Examples[0].Steps[0].out, err, start.Add(10*time.Minute).Unix())
	}
}

func TestCellReportAndReadBack(t *testing.T) {
	e := mkEx("rep",
		Step{
			Trigger: Trigger{Kind: "cron"},
			Expect:  Expect{Reports: []string{"probe.soc"}},
		},
	)
	e = withOp(e, "op_cron", `{"op":"cell_report","name":"soc","json":55}`)
	rep := run(t, testManifest(), opts(), e)
	if !rep.Passed {
		t.Fatalf("report = %+v", rep)
	}
	r := rep.Examples[0].Steps[0].Reports
	if len(r) != 1 || r[0].Cell != "probe.soc" || string(r[0].Value) != "55" {
		t.Fatalf("reports = %+v", r)
	}
}

func TestCellReportWrongType(t *testing.T) {
	e := withOp(mkEx("bad", cron(Expect{})), "op_cron", `{"op":"cell_report","name":"soc","json":"high"}`)
	rep := run(t, testManifest(), opts(), e)
	st := rep.Examples[0].Steps[0]
	if rep.Passed || st.Kind != "module.format" || !st.Ran || st.OK {
		t.Fatalf("step = %+v", st)
	}
}

func TestCellReadBackLater(t *testing.T) {
	// A later step reads the own cell the first one reported: reading a
	// cell that was never set fails the step, so the pass proves the value.
	e := mkEx("rb",
		cron(Expect{}),
		Step{Trigger: Trigger{Kind: "tool", Tool: "status"}},
	)
	e = withOp(e, "op_cron", `{"op":"cell_report","name":"soc","json":55}`)
	e = withOp(e, "op_tool_status", `{"op":"cell_get","name":"soc"}`)
	rep := run(t, testManifest(), opts(), e)
	if !rep.Passed || !strings.Contains(string(rep.Examples[0].Steps[1].out), `"value":55`) {
		t.Fatalf("report = %+v", rep)
	}
}

func TestFetchCanned(t *testing.T) {
	e := withOp(mkEx("f", cron(Expect{})), "op_cron", `{"op":"fetch","url":"https://api.example.com/a"}`)
	e.HTTP = []Canned{{URL: "https://api.example.com/a", Status: 200, Body: "hello"}}
	rep := run(t, testManifest(), opts(), e)
	if o := string(rep.Examples[0].Steps[0].out); !rep.Passed || !strings.Contains(o, `"status":200`) || !strings.Contains(o, `"body":"hello"`) {
		t.Fatalf("canned fetch: %+v", rep)
	}
	other := withOp(mkEx("g", cron(Expect{})), "op_cron", `{"op":"fetch","url":"https://api.example.com/b"}`)
	other.HTTP = e.HTTP
	rep = run(t, testManifest(), opts(), other)
	st := rep.Examples[0].Steps[0]
	if st.OK || st.Kind == "" {
		t.Fatalf("an unlisted url must fail the step: %+v", st)
	}
}

func TestSecretFake(t *testing.T) {
	e := withOp(mkEx("s", cron(Expect{})), "op_cron", `{"op":"secret","name":"TOKEN"}`)
	e.Secrets = map[string]string{"TOKEN": "fake"}
	rep := run(t, testManifest(), opts(), e)
	if !rep.Passed || !strings.Contains(string(rep.Examples[0].Steps[0].out), `"value":"fake"`) {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSpinTimeout(t *testing.T) {
	m := testManifest()
	m.Limits.Timeout = 200 * time.Millisecond
	e := withOp(mkEx("spin", cron(Expect{})), "op_cron", `{"op":"spin"}`)
	rep := run(t, m, opts(), e)
	if st := rep.Examples[0].Steps[0]; st.Kind != "module.timeout" || st.OK {
		t.Fatalf("step = %+v", st)
	}
	e2 := withOp(mkEx("spin", cron(Expect{Fails: true})), "op_cron", `{"op":"spin"}`)
	if rep := run(t, m, opts(), e2); !rep.Passed {
		t.Fatalf("expected failure must pass: %+v", rep)
	}
}

func TestBudget(t *testing.T) {
	e := withOp(mkEx("slow", cron(Expect{}), cron(Expect{}), cron(Expect{})), "op_cron", `{"op":"spin"}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rep, err := Run(ctx, probe(t), testManifest(), "probe", Examples{Examples: []Example{e}}, opts())
	if err != nil {
		t.Fatal(err)
	}
	st := rep.Examples[0].Steps
	if rep.Passed || len(st) != 3 || st[1].Ran || st[2].Ran {
		t.Fatalf("steps = %+v", st)
	}
}

func TestExamplesStartEmpty(t *testing.T) {
	put := withOp(mkEx("one", cron(Expect{})), "op_cron", `{"op":"state_put","name":"k","value":"v"}`)
	get := withOp(mkEx("two", cron(Expect{})), "op_cron", `{"op":"state_get","name":"k"}`)
	// Same example order, with the put first: if state leaked, two sees "v".
	rep := run(t, testManifest(), opts(), put, get)
	if !strings.Contains(string(rep.Examples[1].Steps[0].out), `"ok":false`) {
		t.Fatalf("second example saw the first's state: %s", rep.Examples[1].Steps[0].out)
	}
}

func TestCurrentRun(t *testing.T) {
	o := opts()
	o.Current = map[string]Seeded{"other.level": {Value: json.RawMessage(`7`), At: start.Add(-time.Hour)}}
	bad := withOp(mkEx("ok", cron(Expect{})), "op_cron", `{"op":"notify","value":"hi"}`)
	m := testManifest()
	bad = withOp(bad, "op_cell", `{"op":"fail","value":"boom"}`)
	rep := run(t, m, o, bad)
	if rep.Now == nil || rep.Now.OK || !rep.Now.Ran {
		t.Fatalf("now = %+v", rep.Now)
	}
	if !rep.Passed {
		t.Fatal("the current run must not affect Passed")
	}
	o.Current = nil
	if rep := run(t, m, o, bad); rep.Now != nil {
		t.Fatal("no Current, no Now")
	}
}

func TestModelLine(t *testing.T) {
	rep := Report{Examples: []ExampleResult{
		{Name: "charging crosses 60%", Steps: []StepResult{
			{N: 1, Ran: true, OK: true},
			{N: 2, Ran: true, Mismatch: "expected 1 message, got 0", Messages: []string{"SECRET TEXT"},
				Reports: []Reported{{Cell: "probe.soc", Value: json.RawMessage(`987654`)}}},
		}},
		{Name: "tool answers", Steps: []StepResult{{N: 1, Ran: true, Kind: "module.trap"}}},
		{Name: "fine", Passed: true, Steps: []StepResult{{N: 1, Ran: true, OK: true}}},
	}}
	got := rep.ModelLine()
	for _, w := range []string{"3 examples", "1 passed", `"charging crosses 60%"`, "step 2", "expected 1 message, got 0", "module.trap", `"tool answers"`} {
		if !strings.Contains(got, w) {
			t.Errorf("line lacks %q:\n%s", w, got)
		}
	}
	for _, w := range []string{"SECRET TEXT", "987654"} {
		if strings.Contains(got, w) {
			t.Errorf("line leaks %q:\n%s", w, got)
		}
	}
	if got := (Report{Examples: make([]ExampleResult, 2), Passed: true}).ModelLine(); !strings.Contains(got, "all passed") {
		t.Errorf("passed line = %q", got)
	}
	if got := (Report{Refused: "examples.yaml: no examples"}).ModelLine(); !strings.Contains(got, "refused") {
		t.Errorf("refused line = %q", got)
	}
}
