package restart_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/restart"
	"github.com/igorrius/ganglion/texts"
)

var t0 = time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)

type kit struct {
	path    string
	clock   time.Time
	sent    []event.Outgoing
	applied []string
	appErr  error
}

func newKit(t *testing.T) *kit {
	return &kit{path: filepath.Join(t.TempDir(), restart.FileName), clock: t0}
}

func (k *kit) note() *restart.Note {
	return restart.NewNote(k.path, t0.Add(-time.Hour), func() time.Time { return k.clock })
}

func (k *kit) back() *restart.Back {
	enqueue := func(_ context.Context, ms ...event.Outgoing) error { k.sent = append(k.sent, ms...); return nil }
	lang := func(_ context.Context, p string) (texts.Lang, error) {
		if p == "telegram:user:123456" {
			return "ru", nil
		}
		return texts.English, nil
	}
	applied := func(_ context.Context, since, until time.Time) ([]string, error) {
		if !since.Equal(t0.Add(-time.Hour)) || !until.Equal(t0) {
			return nil, errors.New("wrong window")
		}
		return k.applied, k.appErr
	}
	return restart.NewBack(k.note(), enqueue, lang, applied, texts.Shipped(), func() time.Time { return k.clock },
		slog.New(slog.DiscardHandler))
}

func TestBackTellsOnceWhereItWasAsked(t *testing.T) {
	k := newKit(t)
	if err := k.note().Ask("telegram:user:7", "telegram", "telegram:7"); err != nil {
		t.Fatal(err)
	}
	k.clock = t0.Add(14 * time.Second)
	for range 2 {
		if err := k.back().Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(k.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(k.sent))
	}
	m := k.sent[0]
	if m.Transport != "telegram" || m.Conv != "telegram:7" || !strings.Contains(m.Text, "14 s") || m.IdemKey == "" {
		t.Fatalf("message = %+v", m)
	}
	if _, err := os.Stat(k.path); !os.IsNotExist(err) {
		t.Fatal("the record is still there")
	}
}

func TestBackNamesTheChangesInTheAskersLanguage(t *testing.T) {
	k := newKit(t)
	k.applied = []string{"more room", "debug logs"}
	if err := k.note().Ask("telegram:user:123456", "telegram", "telegram:123456"); err != nil {
		t.Fatal(err)
	}
	k.clock = t0.Add(5 * time.Second)
	if err := k.back().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := k.sent[0].Text
	for _, want := range []string{"Я снова здесь", "5 с", "• more room", "• debug logs"} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
}

func TestBackWithoutARecordSaysNothing(t *testing.T) {
	k := newKit(t)
	if err := k.back().Run(context.Background()); err != nil || len(k.sent) != 0 {
		t.Fatalf("sent %v, %v", k.sent, err)
	}
}

func TestABrokenRecordIsDroppedNotTold(t *testing.T) {
	k := newKit(t)
	if err := os.WriteFile(k.path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := k.back().Run(context.Background()); err != nil || len(k.sent) != 0 {
		t.Fatalf("sent %v, %v", k.sent, err)
	}
	if _, err := os.Stat(k.path); !os.IsNotExist(err) {
		t.Fatal("a broken record stays")
	}
}

func TestBackSaysItIsBackWhenTheChangesCannotBeRead(t *testing.T) {
	k := newKit(t)
	k.appErr = errors.New("no git")
	if err := k.note().Ask("telegram:user:7", "telegram", "telegram:7"); err != nil {
		t.Fatal(err)
	}
	if err := k.back().Run(context.Background()); err != nil || len(k.sent) != 1 || strings.Contains(k.sent[0].Text, "apply") {
		t.Fatalf("sent %+v, %v", k.sent, err)
	}
}
