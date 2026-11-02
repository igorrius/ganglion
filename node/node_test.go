package node

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const testToken = "7001:test-secret"

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{DB: filepath.Join(t.TempDir(), "node.db"), BotAPIURL: "http://127.0.0.1:1"}
}

type running struct {
	cancel func()
	done   chan error
}

func runNode(t *testing.T, roles []Role, cfg Config, logger *slog.Logger) running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- Run(ctx, roles, cfg, logger) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.done:
			_ = err // tests that care check it through result
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	})
	return r
}

func (r running) result(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err // for the cleanup
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil))
	r := runNode(t, []Role{Core, Edge}, testConfig(t), logger)
	time.Sleep(50 * time.Millisecond)
	r.cancel()
	if err := r.result(t); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if !strings.Contains(logs.String(), "roles=core,edge") {
		t.Errorf("startup log does not name the roles:\n%s", logs.String())
	}
}

func TestRunRequiresRoles(t *testing.T) {
	if err := Run(context.Background(), nil, testConfig(t), slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Run with no roles returned nil, want error")
	}
}

func TestEchoOverTelegram(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	runNode(t, []Role{Core}, cfg, slog.New(slog.DiscardHandler))

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	fake.AddMessage(telegramtest.Incoming{ChatID: 66, UserID: 66, Text: "stranger"})
	eventually(t, "the echo", func() bool { return len(fake.Sent()) >= 1 })
	time.Sleep(200 * time.Millisecond)
	sent := fake.Sent()
	if len(sent) != 1 || sent[0] != (telegramtest.Outgoing{ChatID: 42, Text: "echo: hi"}) {
		t.Fatalf("sent %+v, want one echo to the admin and nothing to the stranger", sent)
	}
}

func TestConflictSendsOneAlert(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.SetWebhook(true)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	var logs bytes.Buffer
	r := runNode(t, []Role{Core}, cfg, slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil)))

	eventually(t, "the alert", func() bool { return len(fake.Sent()) >= 1 })
	time.Sleep(300 * time.Millisecond)
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].ChatID != 42 || !strings.Contains(sent[0].Text, "Polling stopped") {
		t.Fatalf("sent %+v, want one alert to the admin", sent)
	}
	if n := fake.Calls("getUpdates"); n != 1 {
		t.Fatalf("getUpdates called %d times, want 1: polling must stop", n)
	}
	select {
	case err := <-r.done:
		t.Fatalf("node exited on 409 (%v); it must keep running", err)
	default:
	}
	r.cancel()
	if err := r.result(t); err != nil {
		t.Fatalf("Run after cancel = %v", err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no error log for the conflict:\n%s", logs.String())
	}
}

// syncWriter serialises writes from several goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func TestWrongTokenEndsRun(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL = "7001:wrong", fake.URL()
	err := runNode(t, []Role{Core}, cfg, slog.New(slog.DiscardHandler)).result(t)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Run = %v, want the 401", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestRestartReleasesDeadLeases(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	// A previous process stored an event and died inside the turn.
	db, err := sqlitedb.Open(ctx, cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(event.Message{Text: "before the crash", From: "telegram:user:42"})
	if err := q.Put(ctx, event.Event{IdemKey: "telegram:7001:1", Source: "telegram", Conv: "telegram:42", Kind: "message", Payload: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Lease(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	fake := telegramtest.NewServer(t, testToken)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	runNode(t, []Role{Core}, cfg, slog.New(slog.DiscardHandler))
	eventually(t, "the echo of the event the dead process held", func() bool {
		sent := fake.Sent()
		return len(sent) == 1 && sent[0].Text == "echo: before the crash"
	})
}
