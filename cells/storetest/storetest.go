// Package storetest is the conformance suite every cells.Store must pass.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func switchCell(id string) cells.Cell {
	return cells.Cell{ID: id, Kind: cells.KindSwitch, Version: 1, Updated: t0}
}

func requestCell(id, status string, expires time.Time) cells.Cell {
	return cells.Cell{
		ID: id, Kind: cells.KindRequest, Version: 1, Updated: t0, Expires: expires,
		Desired:  json.RawMessage(`{"question":"ok?"}`),
		Reported: json.RawMessage(`{"status":"` + status + `"}`),
	}
}

func change(id string, field string, v string) cells.Change {
	return cells.Change{CellID: id, Field: field, Value: json.RawMessage(v), Source: "test", At: t0}
}

// Run runs the suite. newStore must return an empty store.
func Run(t *testing.T, newStore func(t *testing.T) cells.Store) {
	// A deadline turns a hang (for example a deadlock on a single
	// connection) into a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("create and get", func(t *testing.T) {
		s := newStore(t)
		ch, err := s.Create(ctx, switchCell("load.a"), "test")
		if err != nil {
			t.Fatal(err)
		}
		if ch.Seq <= 0 || ch.Field != "created" || ch.Version != 1 || ch.CellID != "load.a" {
			t.Fatalf("create change = %+v", ch)
		}
		c, err := s.Get(ctx, "load.a")
		if err != nil {
			t.Fatal(err)
		}
		if c.ID != "load.a" || c.Kind != cells.KindSwitch || c.Version != 1 || !c.Updated.Equal(t0) {
			t.Fatalf("got %+v", c)
		}
	})

	t.Run("create twice", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Create(ctx, switchCell("load.a"), "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Create(ctx, switchCell("load.a"), "test"); !errors.Is(err, cells.ErrExists) {
			t.Fatalf("second create = %v, want ErrExists", err)
		}
	})

	t.Run("get missing", func(t *testing.T) {
		if _, err := newStore(t).Get(ctx, "nope"); !errors.Is(err, cells.ErrNotFound) {
			t.Fatalf("get missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("update bumps version and sequence", func(t *testing.T) {
		s := newStore(t)
		created, _ := s.Create(ctx, switchCell("load.a"), "test")
		c, _ := s.Get(ctx, "load.a")
		c.Desired = json.RawMessage(`"on"`)
		ch, err := s.Update(ctx, c, 1, change("load.a", "desired", `"on"`))
		if err != nil {
			t.Fatal(err)
		}
		if ch.Seq <= created.Seq || ch.Version != 2 {
			t.Fatalf("change = %+v, want seq > %d and version 2", ch, created.Seq)
		}
		got, _ := s.Get(ctx, "load.a")
		if got.Version != 2 || string(got.Desired) != `"on"` {
			t.Fatalf("stored = %+v", got)
		}
	})

	t.Run("update with stale version", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Create(ctx, switchCell("load.a"), "test")
		c, _ := s.Get(ctx, "load.a")
		c.Desired = json.RawMessage(`"on"`)
		if _, err := s.Update(ctx, c, 1, change("load.a", "desired", `"on"`)); err != nil {
			t.Fatal(err)
		}
		c.Desired = json.RawMessage(`"off"`)
		if _, err := s.Update(ctx, c, 1, change("load.a", "desired", `"off"`)); !errors.Is(err, cells.ErrConflict) {
			t.Fatalf("stale update = %v, want ErrConflict", err)
		}
		got, _ := s.Get(ctx, "load.a")
		if string(got.Desired) != `"on"` || got.Version != 2 {
			t.Fatalf("stale update changed the cell: %+v", got)
		}
		if h, _ := s.History(ctx, "load.a", 10); len(h) != 2 {
			t.Fatalf("history has %d entries, want 2 (created, desired)", len(h))
		}
	})

	t.Run("changes since, in order, with limit", func(t *testing.T) {
		s := newStore(t)
		first, _ := s.Create(ctx, switchCell("load.a"), "test")
		_, _ = s.Create(ctx, switchCell("load.b"), "test")
		a, _ := s.Get(ctx, "load.a")
		a.Desired = json.RawMessage(`"on"`)
		_, _ = s.Update(ctx, a, 1, change("load.a", "desired", `"on"`))

		all, err := s.ChangesSince(ctx, 0, 100)
		if err != nil || len(all) != 3 {
			t.Fatalf("ChangesSince(0) = %d changes (%v), want 3", len(all), err)
		}
		for i := 1; i < len(all); i++ {
			if all[i].Seq <= all[i-1].Seq {
				t.Fatalf("changes out of order: %+v", all)
			}
		}
		after, _ := s.ChangesSince(ctx, first.Seq, 100)
		if len(after) != 2 || after[0].CellID != "load.b" {
			t.Fatalf("ChangesSince(first) = %+v", after)
		}
		if page, _ := s.ChangesSince(ctx, 0, 2); len(page) != 2 {
			t.Fatalf("limit 2 returned %d", len(page))
		}
	})

	t.Run("history newest first", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Create(ctx, switchCell("load.a"), "test")
		c, _ := s.Get(ctx, "load.a")
		c.Desired = json.RawMessage(`"on"`)
		_, _ = s.Update(ctx, c, 1, change("load.a", "desired", `"on"`))
		h, err := s.History(ctx, "load.a", 10)
		if err != nil || len(h) != 2 || h[0].Field != "desired" || h[1].Field != "created" {
			t.Fatalf("history = %+v (%v)", h, err)
		}
		if h1, _ := s.History(ctx, "load.a", 1); len(h1) != 1 {
			t.Fatalf("limit 1 returned %d", len(h1))
		}
	})

	t.Run("pending requests before", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Create(ctx, requestCell("request.due", "pending", t0.Add(time.Minute)), "test")
		_, _ = s.Create(ctx, requestCell("request.later", "pending", t0.Add(time.Hour)), "test")
		_, _ = s.Create(ctx, requestCell("request.done", "approved", t0.Add(time.Minute)), "test")
		_, _ = s.Create(ctx, switchCell("load.a"), "test")
		due, err := s.PendingRequestsBefore(ctx, t0.Add(10*time.Minute))
		if err != nil || len(due) != 1 || due[0].ID != "request.due" {
			t.Fatalf("due = %+v (%v), want only request.due", due, err)
		}
	})

	t.Run("list sorted by id", func(t *testing.T) {
		s := newStore(t)
		for _, id := range []string{"load.b", "load.a", "grid"} {
			_, _ = s.Create(ctx, switchCell(id), "test")
		}
		l, err := s.List(ctx)
		if err != nil || len(l) != 3 || l[0].ID != "grid" || l[1].ID != "load.a" || l[2].ID != "load.b" {
			t.Fatalf("list = %+v (%v)", l, err)
		}
	})

	t.Run("owner and about", func(t *testing.T) {
		s := newStore(t)
		c := cells.Cell{ID: "demo-reader.soc", Kind: cells.KindSensor, Owner: "demo-reader", About: "soc", Version: 1, Updated: t0}
		_, _ = s.Create(ctx, c, "test")
		_, _ = s.Create(ctx, switchCell("load.a"), "test")
		got, err := s.Get(ctx, "demo-reader.soc")
		if err != nil || got.Owner != "demo-reader" || got.About != "soc" {
			t.Fatalf("got %+v (%v)", got, err)
		}
		if err := s.SetOwner(ctx, "load.a", "m", "about"); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Get(ctx, "load.a")
		if got.Owner != "m" || got.About != "about" || got.Version != 1 {
			t.Fatalf("after SetOwner %+v", got)
		}
		if err := s.SetOwner(ctx, "nope", "m", ""); !errors.Is(err, cells.ErrNotFound) {
			t.Fatalf("SetOwner missing = %v", err)
		}
		n, err := s.Disown(ctx, "m")
		if err != nil || n != 1 {
			t.Fatalf("Disown = %d, %v", n, err)
		}
		got, _ = s.Get(ctx, "load.a")
		if got.Owner != "" || got.About != "about" {
			t.Fatalf("after Disown %+v", got)
		}
		if l, _ := s.List(ctx); len(l) != 2 || l[0].Owner != "demo-reader" {
			t.Fatalf("list = %+v", l)
		}
	})

	t.Run("history since", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Create(ctx, switchCell("load.a"), "test")
		_, _ = s.Create(ctx, switchCell("load.b"), "test")
		for i := range 4 {
			ch := change("load.a", "reported", `"on"`)
			ch.At = t0.Add(time.Duration(i+1) * time.Minute)
			_, _ = s.Update(ctx, switchCell("load.a"), int64(i+1), ch)
			_, _ = s.Update(ctx, switchCell("load.b"), int64(i+1), ch)
		}
		got, err := s.HistorySince(ctx, "load.a", t0.Add(2*time.Minute).UnixMilli(), 10)
		if err != nil || len(got) != 3 || got[0].Version != 3 || got[2].Version != 5 {
			t.Fatalf("since = %+v (%v)", got, err)
		}
		for _, c := range got {
			if c.CellID != "load.a" {
				t.Fatalf("foreign change %+v", c)
			}
		}
		if got, _ = s.HistorySince(ctx, "load.a", t0.UnixMilli(), 2); len(got) != 2 || got[0].Version != 1 {
			t.Fatalf("limited = %+v", got)
		}
	})
}
