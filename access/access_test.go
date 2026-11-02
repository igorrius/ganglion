package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newList(t *testing.T) *access.List {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := access.New(ctx, db, access.WithClock(func() time.Time { return t0 }))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func message(from, name, text string) event.Event {
	p, _ := json.Marshal(event.Message{Text: text, From: from, Name: name})
	return event.Event{ID: "01X", IdemKey: "telegram:7001:1", Source: "telegram", Conv: "telegram:42", Kind: "message", Payload: p, At: t0}
}

// recorder is a sink that records what reaches it.
type recorder struct{ got []event.Event }

func (r *recorder) sink(_ context.Context, e event.Event) error {
	r.got = append(r.got, e)
	return nil
}

func TestAllowAndRole(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if _, ok, err := l.Role(ctx, "telegram:user:1"); err != nil || ok {
		t.Fatalf("unknown principal: ok=%v err=%v", ok, err)
	}
	if err := l.Allow(ctx, "telegram:user:1", access.RoleAdmin, "Igor"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(ctx, "telegram:user:2", access.RoleFamily, "Anna"); err != nil {
		t.Fatal(err)
	}
	if r, ok, err := l.Role(ctx, "telegram:user:2"); err != nil || !ok || r != access.RoleFamily {
		t.Fatalf("Role = %q, %v, %v", r, ok, err)
	}
	// Allow is an upsert.
	if err := l.Allow(ctx, "telegram:user:2", access.RoleAdmin, "Anna"); err != nil {
		t.Fatal(err)
	}
	admins, err := l.Admins(ctx)
	if err != nil || !slices.Equal(admins, []string{"telegram:user:1", "telegram:user:2"}) {
		t.Fatalf("Admins = %v, %v", admins, err)
	}
	if err := l.Remove(ctx, "telegram:user:2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Role(ctx, "telegram:user:2"); ok {
		t.Fatal("removed principal still allowed")
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.Role("owner"), ""); !errors.Is(err, access.ErrUnknownRole) {
		t.Fatalf("Allow with role owner: %v, want ErrUnknownRole", err)
	}
	if err := l.Allow(ctx, "", access.RoleFamily, ""); err == nil {
		t.Fatal("Allow with an empty principal succeeded")
	}
}

func TestFilterPassesKnown(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.RoleFamily, "Igor"); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	sink := l.Filter(r.sink, slog.New(slog.DiscardHandler))
	if err := sink(ctx, message("telegram:user:1", "Igor", "hi")); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 {
		t.Fatalf("next got %d events, want 1", len(r.got))
	}
}

func TestFilterDropsStranger(t *testing.T) {
	l := newList(t)
	var logs bytes.Buffer
	r := &recorder{}
	sink := l.Filter(r.sink, slog.New(slog.NewJSONHandler(&logs, nil)))
	const secret = "my card number is 4111"
	if err := sink(context.Background(), message("telegram:user:666", "Stranger", secret)); err != nil {
		t.Fatalf("Filter returned %v, want nil so the offset moves on", err)
	}
	if len(r.got) != 0 {
		t.Fatal("a stranger's message reached the inbox")
	}
	out := logs.String()
	if strings.Contains(out, secret) || strings.Contains(out, `"text"`) {
		t.Fatalf("log has the text: %s", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("one JSON log record expected: %q", out)
	}
	if rec["level"] != "WARN" || rec["principal"] != "telegram:user:666" || rec["name"] != "Stranger" ||
		rec["conv"] != "telegram:42" || rec["text_len"] != float64(len(secret)) {
		t.Fatalf("log record %v", rec)
	}
}

func TestFilterDropsMalformedMessage(t *testing.T) {
	l := newList(t)
	r := &recorder{}
	sink := l.Filter(r.sink, slog.New(slog.DiscardHandler))
	e := message("telegram:user:1", "", "")
	e.Payload = json.RawMessage(`not json`)
	if err := sink(context.Background(), e); err != nil || len(r.got) != 0 {
		t.Fatalf("malformed payload: err=%v passed=%d", err, len(r.got))
	}
}

func TestFilterPassesNonMessages(t *testing.T) {
	l := newList(t)
	r := &recorder{}
	sink := l.Filter(r.sink, slog.New(slog.DiscardHandler))
	e := event.Event{ID: "01Y", IdemKey: "cron:x", Source: "cron", Conv: "cron:x", Kind: "cron.tick", At: t0}
	if err := sink(context.Background(), e); err != nil || len(r.got) != 1 {
		t.Fatalf("non-message: err=%v passed=%d", err, len(r.got))
	}
}

func TestFilterPassesNextError(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.RoleFamily, ""); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("inbox down")
	sink := l.Filter(func(context.Context, event.Event) error { return boom }, slog.New(slog.DiscardHandler))
	if err := sink(ctx, message("telegram:user:1", "", "hi")); !errors.Is(err, boom) {
		t.Fatalf("Filter = %v, want next's error so the update is redelivered", err)
	}
}

func TestFilterCountsDrops(t *testing.T) {
	rec := telemetrytest.Install(t)
	l := newList(t)
	sink := l.Filter((&recorder{}).sink, slog.New(slog.DiscardHandler))
	_ = sink(context.Background(), message("telegram:user:666", "", "x"))
	bad := message("telegram:user:1", "", "")
	bad.Payload = json.RawMessage(`nope`)
	_ = sink(context.Background(), bad)
	if got := rec.Value(t, "ganglion.access.dropped", map[string]string{"reason": "stranger"}); got != 1 {
		t.Errorf("stranger drops %v", got)
	}
	if got := rec.Value(t, "ganglion.access.dropped", map[string]string{"reason": "malformed"}); got != 1 {
		t.Errorf("malformed drops %v", got)
	}
}
