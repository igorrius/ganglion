package sqlstore_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/cells/storetest"
	"github.com/igorrius/ganglion/sqlitedb"
)

func open(t *testing.T, path string) *sqlstore.Store {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := sqlstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSQLStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) cells.Store {
		return open(t, filepath.Join(t.TempDir(), "cells.db"))
	})
}

func TestSQLStoreInMemory(t *testing.T) {
	storetest.Run(t, func(t *testing.T) cells.Store { return open(t, ":memory:") })
}

func TestReopenKeepsState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cells.db")
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	s := open(t, path)
	if _, err := s.Create(ctx, cells.Cell{ID: "load.a", Kind: cells.KindSwitch, Backend: "virtual", Version: 1, Updated: at}, "test"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, "load.a")
	c.Reported, c.ReportedCause = json.RawMessage(`"on"`), cells.CauseExternal
	if _, err := s.Update(ctx, c, 1, cells.Change{Field: cells.FieldReported, Value: c.Reported, Cause: cells.CauseExternal, Source: "test", At: at}); err != nil {
		t.Fatal(err)
	}

	again := open(t, path)
	got, err := again.Get(ctx, "load.a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Backend != "virtual" || string(got.Reported) != `"on"` || got.ReportedCause != cells.CauseExternal || !got.Updated.Equal(at) {
		t.Fatalf("after reopen: %+v", got)
	}
	h, _ := again.History(ctx, "load.a", 10)
	if len(h) != 2 || h[0].Cause != cells.CauseExternal || h[0].Source != "test" {
		t.Fatalf("history after reopen: %+v", h)
	}
}
