package hookrecord_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/hookrecord"
)

var _ telegram.WebhookRecord = (*hookrecord.Store)(nil)

func TestRecordSurvivesARestart(t *testing.T) {
	ctx, path := t.Context(), filepath.Join(t.TempDir(), "node.db")
	open := func() *hookrecord.Store {
		db, err := sqlitedb.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		s, err := hookrecord.New(ctx, db, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	if url, err := s.URL(ctx); err != nil || url != "" {
		t.Fatalf("a new node: %q, %v", url, err)
	}
	for _, url := range []string{"https://a.example/telegram/webhook", "https://b.example/telegram/webhook"} {
		if err := s.Set(ctx, url); err != nil {
			t.Fatal(err)
		}
	}
	s = open()
	if url, err := s.URL(ctx); err != nil || url != "https://b.example/telegram/webhook" {
		t.Fatalf("after a restart: %q, %v", url, err)
	}
	if err := s.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if url, err := s.URL(ctx); err != nil || url != "" {
		t.Fatalf("after Clear: %q, %v", url, err)
	}
}
