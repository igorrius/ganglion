package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

const testToken = "7001:test-secret"

func testConfig(t *testing.T) Bootstrap {
	t.Helper()
	return Bootstrap{DB: filepath.Join(t.TempDir(), "node.db"), BotAPIURL: "http://127.0.0.1:1", Timeouts: testTimeouts()}
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
	if len(sent) != 1 || sent[0] != (telegramtest.Outgoing{MessageID: sent[0].MessageID, ChatID: 42, Text: "echo: hi"}) {
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
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.Agent = AgentSettings{Models: orModels(t, model.URL(), "m1"), Keys: map[string]string{"or": "sk-test"}, DailyBudgetUSD: 1, TZ: time.UTC,
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
	want := "cells.get,cells.list,cells.set,destination.list,destination.remove,destination.set,group.disconnect,host.stats,journal.search,language.set,logs.search,memory.audit,memory.forget,memory.read,memory.recall,memory.write," +
		"message.send,module.build,module.install,module.notify_to,module.read,module.status,module.write,persona.propose,plan.show,plan.step,schedule.create,schedule.delete,schedule.list,schedule.run_now,skill.read,turns.failed,web.fetch,web.search"
	if _, err := exec.LookPath("git"); err != nil {
		want = "cells.get,cells.list,cells.set,destination.list,destination.remove,destination.set,group.disconnect,host.stats,journal.search,language.set,logs.search,message.send,module.notify_to,plan.show,plan.step,schedule.create,schedule.delete,schedule.list," +
			"schedule.run_now,skill.read,turns.failed,web.fetch,web.search"
	}
	// host.exec is on offer only where the sandbox is ready (R-CMD-1).
	if hostSandboxReady() {
		want = strings.Replace(want, "cells.set,", "cells.set,host.exec,", 1)
	}
	if got := strings.Join(offered, ","); got != want {
		t.Fatalf("the admin was offered %q", got)
	}
	// The agent's commands are Telegram's command hints, in English and
	// per language (rulings R43, R45).
	sets := fake.CommandSets()
	if len(sets) != 3 || sets[0].Language != "" || sets[1].Language != "ru" || sets[2].Language != "uk" ||
		sets[0].Commands[0] != (telegramtest.BotCommand{Command: "new", Description: "Start a new conversation"}) {
		t.Fatalf("command hints %+v", sets)
	}
	// /tasks among them, in each language (ruling R47).
	for i, want := range []string{"Reminders and scheduled tasks", "Напоминания и задачи по расписанию", "Нагадування та завдання за розкладом"} {
		if !slices.Contains(sets[i].Commands, telegramtest.BotCommand{Command: "tasks", Description: want}) {
			t.Errorf("set %q lacks /tasks: %+v", sets[i].Language, sets[i].Commands)
		}
	}
}

// The echo bot answers no commands and lists none.
func TestEchoListsNoCommands(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	eventually(t, "the echo", func() bool { return len(fake.Sent()) == 1 })
	if fake.Calls("setMyCommands") != 0 {
		t.Fatal("the echo bot listed commands")
	}
}

// A press over the real core and the fake Bot API (ruling R40): the toast
// and the status line reach Telegram first, then the approved call's
// message, then the question's final line, then the model's answer. A
// second press of the same button gets a toast and runs nothing again.
func TestPressFeedbackOrderOverTelegram(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	model.CallTool("message.send", `{"conv":"telegram:user:42","text":"the effect"}`)
	model.Reply("I asked you.")
	model.Reply("Sent.")
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.Agent = AgentSettings{Models: orModels(t, model.URL(), "m1"), Keys: map[string]string{"or": "sk-test"}, DailyBudgetUSD: 1, TZ: time.UTC,
		Workspace: t.TempDir()}
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "send me a note"})
	eventually(t, "the question", func() bool { return len(fake.Sent()) == 2 })
	var question telegramtest.Shown
	for _, m := range fake.Showing() {
		if m.Keyboard != "" {
			question = m
		}
	}
	var rows [][]struct {
		Text string `json:"text"`
		Data string `json:"callback_data"`
	}
	if err := json.Unmarshal([]byte(question.Keyboard), &rows); err != nil || len(rows) != 2 || rows[0][0].Text != "✅ Approve" {
		t.Fatalf("question %+v: %v", question, err)
	}
	before := len(fake.Methods())
	fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 42, Name: "Igor", MessageID: question.MessageID, Data: rows[0][0].Data})
	eventually(t, "the model's answer", func() bool { return len(fake.Sent()) == 4 })
	var did []string
	for _, m := range fake.Methods()[before:] {
		if m != "sendChatAction" {
			did = append(did, m)
		}
	}
	want := "answerCallbackQuery,editMessageText,sendMessage,editMessageText,sendMessage"
	if got := strings.Join(did, ","); got != want {
		t.Fatalf("Bot API calls %s, want %s", got, want)
	}
	edits, sent := fake.Edits(), fake.Sent()
	if toasts := fake.Toasts(); len(toasts) != 1 || toasts[0].Text != "Approved" {
		t.Fatalf("toasts %+v", toasts)
	}
	if !strings.HasPrefix(edits[0].Text, "⏳ Approved, running…") || edits[0].Keyboard != "" ||
		!strings.HasPrefix(edits[1].Text, "Approved, ") || sent[2].Text != "the effect" || sent[3].Text != "Sent." {
		t.Fatalf("edits %+v, sent %+v", edits, sent)
	}
	// A double press: a toast, nothing runs again.
	fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 42, Name: "Igor", MessageID: question.MessageID, Data: rows[0][0].Data})
	eventually(t, "the second toast", func() bool { return len(fake.Toasts()) == 2 })
	if got := fake.Toasts()[1].Text; got != "Already handled." || len(fake.Sent()) != 4 {
		t.Fatalf("second press: toast %q, sent %d", got, len(fake.Sent()))
	}
}

// rootKernel is a kernel that runs as root, so the sandbox is off.
type rootKernel struct{}

func (rootKernel) Euid() int                 { return 0 }
func (rootKernel) LandlockABI() (int, error) { return 0, nil }
func (rootKernel) Dumpable() (bool, error)   { return false, nil }

// R-CMD-5: with the sandbox off, a node of 128 MB still tells its admin the
// machine's numbers, since host.stats starts no subprocess.
func TestHostStatsWithoutTheSandbox(t *testing.T) {
	i := New(context.Background(), agentConfig(t), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	do.OverrideValue(i, sandbox.New("/nonexistent", sandbox.Policy{}, rootKernel{}, time.Now()))
	reg, err := do.Invoke[*tool.Registry](i)
	if err != nil {
		t.Fatal(err)
	}
	p, err := do.Invoke[*policy.Policy](i)
	if err != nil {
		t.Fatal(err)
	}
	run := p.Middleware()(reg.Run)
	c := tool.Call{TurnID: "01EV1", N: 1, Conv: "telegram:42", Transport: "telegram", Principal: "telegram:user:42", Admin: true,
		Name: "host.stats", Args: json.RawMessage(`{}`)}
	res, err := run(context.Background(), c)
	// Memory and load come from /proc, so off Linux the answer says they are not known.
	want := "Machine memory and load: not known here"
	if runtime.GOOS == "linux" {
		want = "Memory:"
	}
	if err != nil || !strings.Contains(res.Text, want) || !strings.Contains(res.Text, "The node's own files:") {
		t.Fatalf("host.stats: %+v, %v; want %q", res, err, want)
	}
	// R-CMD-3, fail closed: with the sandbox off host.exec is not offered,
	// and a call the model makes up for it anyway runs nothing.
	if reg.Offered("host.exec", true) {
		t.Fatal("host.exec is offered with the sandbox off")
	}
	c.N, c.Name, c.Args = 2, "host.exec", json.RawMessage(`{"argv":["df","-h"],"reason":"space"}`)
	if res, err := run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "commands are off") {
		t.Fatalf("host.exec with the sandbox off: %+v, %v", res, err)
	}
}

// freeAddr is a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// R-DLV-18: with telegram.updates: webhook the node checks its public
// address, registers the webhook and answers what Telegram posts to it;
// nobody polls. The listener is the node's one public entry, so the test
// is also its abuse test: a forged update and a probe get nothing.
func TestEchoOverTelegramWebhook(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.HTTPAddr = freeAddr(t)
	cfg.PublicURL, cfg.TelegramWebhook = "http://"+cfg.HTTPAddr, true
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))

	eventually(t, "the webhook registered", func() bool { return len(fake.WebhookSets()) == 1 })
	hook := cfg.PublicURL + telegram.WebhookPath
	if set := fake.WebhookSets()[0]; set.URL != hook || set.Secret == "" || set.MaxConnections != 1 {
		t.Fatalf("setWebhook %+v", set)
	}
	forged := `{"update_id":9,"message":{"message_id":9,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"first_name":"A"},"text":"forged"}}`
	for path, want := range map[string]int{telegram.WebhookPath: http.StatusForbidden, "/metrics": http.StatusNotFound, "/": http.StatusNotFound} {
		resp, err := http.Post(cfg.PublicURL+path, "application/json", strings.NewReader(forged)) //nolint:noctx // a test
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST %s without the secret: %d, want %d", path, resp.StatusCode, want)
		}
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "hi"})
	fake.AddMessage(telegramtest.Incoming{ChatID: 66, UserID: 66, Text: "stranger"})
	eventually(t, "the echo", func() bool { return len(fake.Sent()) >= 1 })
	eventually(t, "both updates confirmed", func() bool { return fake.WebhookInfo().PendingUpdateCount == 0 })
	time.Sleep(200 * time.Millisecond)
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].ChatID != 42 || sent[0].Text != "echo: hi" {
		t.Fatalf("sent %+v, want one echo to the admin: nothing for the stranger or the forged update", sent)
	}
	if n := fake.Calls("getUpdates"); n != 0 {
		t.Errorf("getUpdates called %d times in webhook mode", n)
	}
}

// Set back to polling, the node removes the webhook it set at the last
// start and polls what waited.
func TestBackToPollingRemovesTheNodesWebhook(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	cfg := testConfig(t)
	cfg.BotToken, cfg.BotAPIURL, cfg.Admin = testToken, fake.URL(), "telegram:user:42"
	cfg.HTTPAddr = freeAddr(t)
	cfg.PublicURL, cfg.TelegramWebhook = "http://"+cfg.HTTPAddr, true
	first := runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))
	eventually(t, "the webhook registered", func() bool { return len(fake.WebhookSets()) == 1 })
	first.cancel()
	_ = first.result(t)

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "while away"})
	cfg.TelegramWebhook = false
	runNode(t, []node.Role{node.Core}, cfg, slog.New(slog.DiscardHandler))
	eventually(t, "the echo", func() bool { return len(fake.Sent()) >= 1 })
	if sent := fake.Sent(); sent[0].Text != "echo: while away" {
		t.Errorf("sent %+v, want the echo of what waited", sent)
	}
	if info := fake.WebhookInfo(); info.URL != "" || fake.Calls("deleteWebhook") != 1 {
		t.Errorf("webhook now %q after %d deleteWebhook calls", info.URL, fake.Calls("deleteWebhook"))
	}
}
