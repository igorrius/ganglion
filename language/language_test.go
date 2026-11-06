package language

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

func TestNoticeOncePerLanguage(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		lang  texts.Lang
		event string
		want  bool
	}{
		{"pl", "e1", true},
		{"pl", "e1", true}, // a rerun of the same turn writes the same message
		{"pl", "e2", false},
		{"de", "e3", true},
		{"pl", "e4", false},
	} {
		got, err := s.NoticeFor(ctx, person, tc.lang, tc.event)
		if err != nil || got != tc.want {
			t.Errorf("%s %s: %v, %v; want %v", tc.lang, tc.event, got, err, tc.want)
		}
	}
}

func TestLoadFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pl.yaml", "toast.used: Już załatwione.\ntoast.expired: https://evil.example\nreply.resumed: bez wartości\n")
	write("xx.yaml", "toast.used: nope\n")
	write("de.yaml", ":: not yaml ::\n\t- x")
	write("notes.txt", "toast.used: nope\n")
	var logs bytes.Buffer
	files := LoadFiles(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	if len(files) != 1 || files["pl"][texts.ToastUsed] != "Już załatwione." {
		t.Fatalf("%+v", files)
	}
	if _, ok := files["pl"][texts.ToastExpired]; ok {
		t.Error("a text with an address was accepted")
	}
	if _, ok := files["pl"][texts.ReplyResumed]; ok {
		t.Error("a text without its placeholder was accepted")
	}
	for _, want := range []string{"toast.expired", "reply.resumed", "xx.yaml", "de.yaml"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not name %s:\n%s", want, logs.String())
		}
	}
}

func TestLoadFilesNoDirectory(t *testing.T) {
	if got := LoadFiles(filepath.Join(t.TempDir(), "none"), slog.New(slog.DiscardHandler)); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestDirOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "workspace")
	for _, tc := range []struct {
		dir string
		ok  bool
	}{
		{filepath.Join(root, "languages"), true},
		{filepath.Join(root, "workspace2"), true},
		{ws, false},
		{filepath.Join(ws, "languages"), false},
		{filepath.Join(ws, "..", "workspace", "x"), false},
	} {
		if err := CheckOutside(tc.dir, ws); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.dir, err)
		}
	}
}
