package runner_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/igorrius/ganglion/runner"
)

func TestMemCellsKeepsTheLastReportWithItsTime(t *testing.T) {
	at := time.Unix(1_793_000_000, 0)
	c := runner.NewMemCells(func() time.Time { return at })
	ctx := context.Background()
	if _, _, err := c.Get(ctx, "m.soc"); err == nil {
		t.Fatal("an unreported cell was found")
	}
	for _, v := range []string{"57", "58"} {
		if err := c.Report(ctx, "m.soc", json.RawMessage(v), "m"); err != nil {
			t.Fatal(err)
		}
	}
	v, got, err := c.Get(ctx, "m.soc")
	if err != nil || string(v) != "58" || !got.Equal(at) {
		t.Fatalf("Get = %s, %v, %v; want 58 at %v", v, got, err, at)
	}
	if all := c.Values(); len(all) != 1 || string(all["m.soc"]) != "58" {
		t.Fatalf("Values = %v", all)
	}
}
