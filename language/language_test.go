package language

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
)

const person = "telegram:user:1"

type clock struct{ now time.Time }

func (c *clock) time() time.Time { return c.now }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "language.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{now: time.UnixMilli(1000)}
	s, err := New(ctx, db, WithClock(c.time))
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func resolve(t *testing.T, s *Store, principal string) texts.Lang {
	t.Helper()
	l, err := s.Resolve(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestResolveOrder(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if got := resolve(t, s, person); got != texts.English {
		t.Errorf("nobody known: %s", got)
	}
	if err := s.Seen(ctx, person, "uk"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, s, person); got != "uk" {
		t.Errorf("seen: %s", got)
	}
	if _, err := s.Choose(ctx, person, "pl"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, s, person); got != "pl" {
		t.Errorf("chosen: %s", got)
	}
}

func TestNoPersonIsEnglish(t *testing.T) {
	s, _ := newStore(t)
	if got := resolve(t, s, ""); got != texts.English {
		t.Errorf("no person behind a message: %s", got)
	}
}

func TestSeenIgnoresGarbage(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.Seen(ctx, person, "uk"); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", "xx", strings.Repeat("a", 1000)} {
		if err := s.Seen(ctx, person, code); err != nil {
			t.Fatal(err)
		}
		if got := resolve(t, s, person); got != "uk" {
			t.Errorf("after %.10q: %s", code, got)
		}
	}
	wanted, err := s.Wanted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(wanted, []texts.Lang{"uk"}) {
		t.Errorf("wanted: %v", wanted)
	}
}

func TestSeenNormalizes(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Seen(context.Background(), person, "pt-BR"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, s, person); got != "pt" {
		t.Errorf("%s", got)
	}
}

func TestSeenDoesNotOverrideChosen(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.Choose(ctx, person, "pl"); err != nil {
		t.Fatal(err)
	}
	if err := s.Seen(ctx, person, "uk"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, s, person); got != "pl" {
		t.Errorf("%s", got)
	}
	if l, ok, err := s.Chosen(ctx, person); err != nil || !ok || l != "pl" {
		t.Errorf("Chosen = %s, %v, %v", l, ok, err)
	}
}

func TestChosenNone(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.Seen(ctx, person, "uk"); err != nil {
		t.Fatal(err)
	}
	if l, ok, err := s.Chosen(ctx, person); err != nil || ok || l != "" {
		t.Errorf("Chosen = %q, %v, %v", l, ok, err)
	}
}

func TestChooseReportsChange(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for i, want := range []bool{true, false} {
		changed, err := s.Choose(ctx, person, "pl")
		if err != nil || changed != want {
			t.Errorf("call %d: changed %v, err %v", i, changed, err)
		}
	}
}

func updatedMs(t *testing.T, s *Store) int64 {
	t.Helper()
	var ms int64
	if err := s.db.QueryRow(`SELECT updated_ms FROM people WHERE principal = ?`, person).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestSeenWritesOnlyOnChange(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	if err := s.Seen(ctx, person, "uk"); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(time.Hour)
	if err := s.Seen(ctx, person, "uk-UA"); err != nil {
		t.Fatal(err)
	}
	if got := updatedMs(t, s); got != 1000 {
		t.Errorf("same language rewrote the row: %d", got)
	}
	if err := s.Seen(ctx, person, "ru"); err != nil {
		t.Fatal(err)
	}
	if got := updatedMs(t, s); got == 1000 {
		t.Error("a new language did not write the row")
	}
}

func TestWantedCountsChosenOverSeen(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.Seen(ctx, "a", "uk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Seen(ctx, "b", "uk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Seen(ctx, "c", "de"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Choose(ctx, "c", "pl"); err != nil {
		t.Fatal(err)
	}
	wanted, err := s.Wanted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(wanted)
	if !slices.Equal(wanted, []texts.Lang{"pl", "uk"}) {
		t.Errorf("wanted: %v", wanted)
	}
}
