package cells_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
)

// failingStore refuses Create after limit successful ones: a start that dies
// halfway.
type failingStore struct {
	cells.Store
	limit int
}

func (f *failingStore) Create(ctx context.Context, c cells.Cell, src string) (cells.Change, error) {
	if f.limit == 0 {
		return cells.Change{}, errors.New("crash")
	}
	f.limit--
	return f.Store.Create(ctx, c, src)
}

func oldStore(t *testing.T, ids ...string) cells.Store {
	t.Helper()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := cells.NewMemStore()
	for _, id := range ids {
		c := cells.Cell{ID: id, Kind: cells.KindSensor, Version: 7, Updated: at, Owner: "m", About: "x"}
		if _, err := s.Create(context.Background(), c, "old"); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestMigrateCopiesEveryCell(t *testing.T) {
	ctx := context.Background()
	to := cells.NewMemStore()
	n, err := cells.Migrate(ctx, oldStore(t, "a", "b"), to, slog.New(slog.DiscardHandler))
	if err != nil || n != 2 {
		t.Fatalf("= %d, %v; want 2", n, err)
	}
	c, _ := to.Get(ctx, "a")
	if c.Version != 7 || c.Owner != "m" || c.About != "x" {
		t.Fatalf("copied %+v", c)
	}
	h, _ := to.History(ctx, "a", 10)
	if len(h) != 1 || h[0].Source != cells.MigrationSource {
		t.Fatalf("history = %+v, want one change from migration", h)
	}
}

// A run that dies after some cells, then runs again, ends with every cell
// once, and a value written in between stays.
func TestMigrateAfterACrashFinishesAndKeepsNewerValues(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	from := oldStore(t, "a", "b", "c", "d")
	to := cells.NewMemStore()
	if _, err := cells.Migrate(ctx, from, &failingStore{Store: to, limit: 2}, log); err == nil {
		t.Fatal("the crash was not seen")
	}
	a, _ := to.Get(ctx, "a")
	a.Reported = []byte(`"newer"`)
	if _, err := to.Update(ctx, a, a.Version, cells.Change{Field: cells.FieldReported, Source: "test", At: a.Updated}); err != nil {
		t.Fatal(err)
	}
	n, err := cells.Migrate(ctx, from, to, log)
	if err != nil || n != 2 {
		t.Fatalf("rerun = %d, %v; want the 2 missing", n, err)
	}
	all, _ := to.List(ctx)
	if len(all) != 4 {
		t.Fatalf("%d cells, want 4", len(all))
	}
	if a, _ := to.Get(ctx, "a"); string(a.Reported) != `"newer"` || a.Version != 8 {
		t.Fatalf("the rerun overwrote a: %+v", a)
	}
}
