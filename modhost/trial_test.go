package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/secrets"
)

const trialOK = `
examples:
  - name: echo on a cell
    steps:
      - cells: {p.soc: 5}
        trigger: cell
        expect: {messages: 0}
`

// A trial parses the examples, runs them and also runs once on the node's
// real cells of cells.read (a cell the node lacks is left out) without
// writing a cell, a state row or a notify count.
func TestTrialRunsExamplesAndTheCurrentCellsWithoutChangingTheNode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.cells.DefineOwned(ctx, cells.OwnedDef{ID: "p.soc", Owner: "local/p", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	before, err := e.cells.Report(ctx, "p.soc", json.RawMessage("7"), cells.CauseExternal, "module:local/p")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := e.host.Trial(ctx, build(t, listenerManifest("p.soc, p.grid")), []byte(trialOK), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || len(rep.Examples) != 1 || !rep.Examples[0].Passed {
		t.Fatalf("report %+v", rep)
	}
	if rep.Now == nil || !rep.Now.OK {
		t.Fatalf("the run on the current cells: %+v", rep.Now)
	}
	after, err := e.cells.Get(ctx, "p.soc")
	if err != nil || after.Version != before.Version || string(after.Reported) != "7" {
		t.Fatalf("the node's cell changed: %+v %v", after, err)
	}
	if _, err := e.cells.Get(ctx, "probe.x"); !errors.Is(err, cells.ErrNotFound) {
		t.Fatalf("the trial created a cell: %v", err)
	}
	if len(e.store.counts) != 0 || len(e.store.notified) != 0 {
		t.Fatal("the trial counted a run or a message")
	}
	if v, _ := e.state.Get("probe", "last_cell"); v != "" {
		t.Fatalf("the trial wrote the node's state: %q", v)
	}
}

// A module without a cell trigger has no run on the current cells.
func TestTrialHasNoNowRunWithoutACellTrigger(t *testing.T) {
	e := newEnv(t)
	rep, err := e.host.Trial(context.Background(), build(t, probeManifest), []byte("examples:\n  - name: t\n    steps:\n      - trigger: {tool: status}\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Now != nil {
		t.Fatalf("report %+v", rep)
	}
}

// An example's own config value wins over the manifest's default, and the
// node's settings do not apply to an example.
func TestTrialExampleConfigWinsAndNodeSettingsAreIgnored(t *testing.T) {
	e := newEnv(t)
	b := build(t, probeManifest)
	plain := []byte("examples:\n  - name: word\n    steps:\n      - trigger: {tool: word}\n")
	failing := []byte("examples:\n  - name: word\n    config: {op_tool_word: '{\"op\":\"fail\",\"value\":\"" + hostile + "\"}'}\n    steps:\n      - trigger: {tool: word}\n")
	settings := map[string]any{"op_tool_word": `{"op":"fail","value":"x"}`}
	rep, err := e.host.Trial(context.Background(), b, plain, settings)
	if err != nil || !rep.Passed {
		t.Fatalf("node settings applied to an example: %+v %v", rep, err)
	}
	rep, err = e.host.Trial(context.Background(), b, failing, nil)
	if err != nil || rep.Passed {
		t.Fatalf("example config ignored: %+v %v", rep, err)
	}
	if line := rep.ModelLine(); !strings.Contains(line, "word") || strings.Contains(line, "IGNORE") {
		t.Fatalf("model line %q", line)
	}
}

// A countingSource is a secret store that counts every read.
type countingSource struct{ reads int }

func (c *countingSource) Secret(string) (string, error) { c.reads++; return "hunter2-hunter2", nil }

// A trial reads no secret of the node's.
func TestTrialReadsNoSecret(t *testing.T) {
	e := newEnv(t)
	src := &countingSource{}
	e.own = map[string]secrets.Source{"local/probe": src}
	file := []byte("examples:\n  - name: w\n    secrets: {PROBE_TOKEN: fake}\n    steps:\n      - trigger: {tool: word}\n")
	if _, err := e.host.Trial(context.Background(), build(t, probeManifest), file, nil); err != nil {
		t.Fatal(err)
	}
	if src.reads != 0 {
		t.Fatalf("%d secret reads", src.reads)
	}
}

// A refused examples file is a report, not an error; no file is a pass
// with nothing run.
func TestTrialRefusedFileAndNoFile(t *testing.T) {
	e := newEnv(t)
	b := build(t, probeManifest)
	rep, err := e.host.Trial(context.Background(), b, []byte("examples: []\n"), nil)
	if err != nil || rep.Passed || rep.Refused == "" {
		t.Fatalf("refused: %+v %v", rep, err)
	}
	rep, err = e.host.Trial(context.Background(), b, nil, nil)
	if err != nil || !rep.Passed || len(rep.Examples) != 0 || rep.Now != nil {
		t.Fatalf("none: %+v %v", rep, err)
	}
}

// A build that does not load is the node's words, not the runner's.
func TestTrialNamesALoadFailureInTheNodesWords(t *testing.T) {
	e := newEnv(t)
	b := build(t, probeManifest)
	b.Wasm = []byte("\x00asm\x01\x00\x00\x00" + hostile)
	_, err := e.host.Trial(context.Background(), b, []byte("examples:\n  - name: t\n    steps:\n      - trigger: {tool: status}\n"), nil)
	if !errors.Is(err, modhost.ErrCheckFailed) || strings.Contains(err.Error(), "IGNORE") {
		t.Fatalf("err %v", err)
	}
}
