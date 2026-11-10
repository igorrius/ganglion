package modstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/clock"
)

// R-RUN-16: a module's messages are counted per day; another module and
// another day have their own counts.
func TestCountNotifyCountsPerModuleAndDay(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	day := clock.DayStart(t0, time.UTC)
	for want := 1; want <= 3; want++ {
		n, limited, err := s.CountNotify(ctx, "local/a", day)
		if err != nil || n != want || limited {
			t.Fatalf("count %d: %d, %v, %v", want, n, limited, err)
		}
	}
	if n, _, err := s.CountNotify(ctx, "local/b", day); err != nil || n != 1 {
		t.Fatalf("another module: %d, %v", n, err)
	}
	if n, err := s.NotifiedOn(ctx, "local/a", day); err != nil || n != 3 {
		t.Fatalf("notified on: %d, %v", n, err)
	}
	if n, err := s.NotifiedOn(ctx, "local/a", day.AddDate(0, 0, 1)); err != nil || n != 0 {
		t.Fatalf("a day with none: %d, %v", n, err)
	}
	if n, err := s.NotifiedOn(ctx, "local/none", day); err != nil || n != 0 {
		t.Fatalf("a module with none: %d, %v", n, err)
	}
}

// The day is the node's: in Kyiv 23:59 and 00:01 are two days, though they
// are one day in UTC.
func TestANewDayStartsAtZeroInTheNodesZone(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	late := time.Date(2026, 11, 9, 23, 59, 0, 0, kyiv)
	early := time.Date(2026, 11, 10, 0, 1, 0, 0, kyiv)
	if late.UTC().Day() != early.UTC().Day() {
		t.Fatal("the test means one UTC day")
	}
	for range 2 {
		if _, _, err := s.CountNotify(ctx, "local/a", clock.DayStart(late, kyiv)); err != nil {
			t.Fatal(err)
		}
	}
	n, _, err := s.CountNotify(ctx, "local/a", clock.DayStart(early, kyiv))
	if err != nil || n != 1 {
		t.Fatalf("the new day's first message: %d, %v", n, err)
	}
	if n, _ := s.NotifiedOn(ctx, "local/a", clock.DayStart(late, kyiv)); n != 2 {
		t.Fatalf("the day before: %d", n)
	}
}

// MarkLimited marks the day once; CountNotify reports it from then on, and
// a new day starts unmarked.
func TestMarkLimitedMarksTheDay(t *testing.T) {
	ctx := context.Background()
	s := newEnv(t).open(t)
	day := clock.DayStart(t0, time.UTC)
	if _, limited, _ := s.CountNotify(ctx, "local/a", day); limited {
		t.Fatal("limited before the mark")
	}
	for range 2 {
		if err := s.MarkLimited(ctx, "local/a", day); err != nil {
			t.Fatal(err)
		}
	}
	n, limited, err := s.CountNotify(ctx, "local/a", day)
	if err != nil || n != 2 || !limited {
		t.Fatalf("after the mark: %d, %v, %v", n, limited, err)
	}
	if _, limited, _ := s.CountNotify(ctx, "local/a", day.AddDate(0, 0, 1)); limited {
		t.Fatal("the next day starts limited")
	}
}
