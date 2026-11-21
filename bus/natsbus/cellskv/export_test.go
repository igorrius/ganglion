package cellskv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/bus/natsbus/cellskv"
	"github.com/igorrius/ganglion/cells"
)

// openBus is an embedded bus with the bucket made, and a store on it.
func openBus(t *testing.T) (*natsbus.Bus, *cellskv.Store) {
	t.Helper()
	ctx := context.Background()
	b, err := natsbus.Open(ctx, natsbus.DefaultConfig(""), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := cellskv.New(ctx, b.JetStream())
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}

// withoutSeq drops the revisions, which an import makes new.
func withoutSeq(changes []cells.Change) []cells.Change {
	out := make([]cells.Change, len(changes))
	for i, c := range changes {
		c.Seq = 0
		out[i] = c
	}
	return out
}

// A bucket exported and imported into a fresh bus reads the same through the
// store: the cells, their versions, owners and the history, newest first.
func TestExportImportKeepsCellsAndHistory(t *testing.T) {
	ctx := context.Background()
	from, src := openBus(t)
	ids := []string{"lamp", "load.a", "a:b", "ключ"}
	for _, id := range ids {
		if _, err := src.Create(ctx, sw(id), "test"); err != nil {
			t.Fatal(err)
		}
	}
	for v := int64(1); v <= 5; v++ {
		c := sw("lamp")
		c.Desired = json.RawMessage(`"on"`)
		if _, err := src.Update(ctx, c, v, cells.Change{Field: cells.FieldDesired, Value: c.Desired, Source: "test", At: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := src.Update(ctx, sw("load.a"), v, cells.Change{Field: cells.FieldReported, Value: json.RawMessage(`1`), At: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.SetOwner(ctx, "lamp", "mod", "about it"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	n, err := from.ExportKV(ctx, bus.BucketCells, &out)
	if err != nil || n != 4+10+1 {
		t.Fatalf("export: %d lines, %v", n, err)
	}

	to, dst := openBus(t)
	if m, err := to.ImportKV(ctx, bus.BucketCells, &out); err != nil || m != n {
		t.Fatalf("import: %d lines, %v", m, err)
	}
	want, err := src.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.List(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cells = %+v (%v), want %+v", got, err, want)
	}
	for _, id := range ids {
		hs, err := src.History(ctx, id, 100)
		if err != nil {
			t.Fatal(err)
		}
		hd, err := dst.History(ctx, id, 100)
		if err != nil || !reflect.DeepEqual(withoutSeq(hd), withoutSeq(hs)) {
			t.Fatalf("history of %q = %+v (%v), want %+v", id, hd, err, hs)
		}
	}
	// The imported bucket takes writes on from the import.
	if _, err := dst.Update(ctx, sw("lamp"), 6, cells.Change{Field: cells.FieldDesired, Value: json.RawMessage(`"off"`), At: t0}); err != nil {
		t.Fatalf("a write after the import: %v", err)
	}
}

// An import refuses a bucket that holds entries and writes nothing.
func TestImportRefusesABucketWithCells(t *testing.T) {
	ctx := context.Background()
	from, src := openBus(t)
	if _, err := src.Create(ctx, sw("lamp"), "test"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := from.ExportKV(ctx, bus.BucketCells, &out); err != nil {
		t.Fatal(err)
	}
	to, dst := openBus(t)
	if _, err := dst.Create(ctx, sw("other"), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := to.ImportKV(ctx, bus.BucketCells, &out); !errors.Is(err, natsbus.ErrNotEmpty) {
		t.Fatalf("error %v, want ErrNotEmpty", err)
	}
	if all, _ := dst.List(ctx); len(all) != 1 || all[0].ID != "other" {
		t.Fatalf("the bucket changed: %+v", all)
	}
}
