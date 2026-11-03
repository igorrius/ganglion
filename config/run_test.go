package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const testToken = "7001:test-secret"

func testConfig(t *testing.T) Bootstrap {
	t.Helper()
	return Bootstrap{DB: filepath.Join(t.TempDir(), "node.db"), BotAPIURL: "http://127.0.0.1:1"}
}

type running struct {
	cancel func()
	done   chan error
}

func runNode(t *testing.T, roles []node.Role, cfg Bootstrap, logger *slog.Logger) running {
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
	r := runNode(t, []node.Role{node.Core, node.Edge}, testConfig(t), logger)
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
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))

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
	r := runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil)))

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
	err := runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler)).result(t)
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
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))
	eventually(t, "the echo of the event the dead process held", func() bool {
		sent := fake.Sent()
		return len(sent) == 1 && sent[0].Text == "echo: before the crash"
	})
}

func TestNoTransportKeepsRepliesPending(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, event.Outgoing{IdemKey: "r1", Transport: "telegram", Conv: "telegram:42", Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	r := runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler)) // no token: no transport
	time.Sleep(300 * time.Millisecond)
	r.cancel()
	if err := r.result(t); err != nil {
		t.Fatal(err)
	}

	db, err = sqlitedb.Open(ctx, cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var status string
	if err := db.QueryRow(`SELECT status FROM outbox WHERE idem_key = 'r1'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("reply status %q (%v) after a start without a token, want pending", status, err)
	}
}

func TestSecondCoreOnSameDBRefused(t *testing.T) {
	cfg := testConfig(t)
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))
	time.Sleep(200 * time.Millisecond)
	err := runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler)).result(t)
	if err == nil || !strings.Contains(err.Error(), "another") || !strings.Contains(err.Error(), cfg.DB) {
		t.Fatalf("second core on %s: %v, want a refusal naming the database", cfg.DB, err)
	}
}

func TestAgentOverTelegram(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	model.Reply("Привет, Igor!")
	catalog, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: or, type: openai, base_url: " + model.URL() +
		", key_env: OR_KEY}\nmodels:\n  - {provider: or, name: m1, tier: smart, tools: true, context: 64000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.Agent = AgentSettings{Catalog: &catalog, Keys: map[string]string{"or": "sk-test"}, DailyBudgetUSD: 1, TZ: time.UTC,
		Workspace: t.TempDir()}
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "hi"})
	eventually(t, "the model's reply", func() bool { return len(fake.Sent()) == 1 })
	if got := fake.Sent()[0]; got.Text != "Привет, Igor!" || got.ChatID != 42 {
		t.Fatalf("sent %+v", got)
	}
	reqs := model.Requests()
	last := reqs[0].Messages[len(reqs[0].Messages)-1]
	content, _ := last["content"].(string)
	if reqs[0].Model != "m1" || !strings.HasPrefix(content, "Igor: hi\n[now: ") || !strings.HasSuffix(content, ", UTC]") {
		t.Fatalf("model request %+v", reqs[0])
	}
	var offered []string
	for _, tl := range reqs[0].Tools {
		offered = append(offered, tl["function"].(map[string]any)["name"].(string))
	}
	want := "cells.get,cells.set,memory.audit,memory.forget,memory.read,memory.recall,memory.write,message.send," +
		"schedule.create,schedule.delete,schedule.list,schedule.run_now,web.fetch,web.search"
	if _, err := exec.LookPath("git"); err != nil {
		want = "cells.get,cells.set,message.send,schedule.create,schedule.delete,schedule.list,schedule.run_now,web.fetch,web.search"
	}
	if got := strings.Join(offered, ","); got != want {
		t.Fatalf("the admin was offered %q", got)
	}
}
