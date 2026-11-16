package modhost

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/runner"
)

// The seeds of the run on the current cells are the module's readable cells
// the node has with a value; a cell the node lacks, or one never reported,
// is left out, and a cell the module may not read is never read.
func TestCurrentCellsLeaveOutWhatTheNodeLacks(t *testing.T) {
	ctx := context.Background()
	svc := cells.NewService(cells.NewMemStore())
	for _, id := range []string{"p.soc", "p.empty", "p.other"} {
		if err := svc.DefineOwned(ctx, cells.OwnedDef{ID: id, Owner: "local/p", Kind: cells.KindSensor}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Report(ctx, "p.soc", json.RawMessage("7"), cells.CauseExternal, "module:local/p"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Report(ctx, "p.other", json.RawMessage("1"), cells.CauseExternal, "module:local/p"); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Cells: svc, Log: slog.New(slog.DiscardHandler)})
	m := runner.Manifest{Cells: runner.Cells{Read: []string{"p.soc", "p.grid", "p.empty"}}}
	got := h.currentCells(ctx, m)
	if len(got) != 1 || string(got["p.soc"].Value) != "7" {
		t.Fatalf("seeds %v", got)
	}
}
