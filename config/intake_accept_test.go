package config

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/internal/testinputs"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/runner/runnertest"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

// intakeSecret is the value the owner hands over in the abuse test.
const intakeSecret = "chat-handed-secret-5521"

// cardLink finds the page link among a secret request's card's entities.
var cardLink = regexp.MustCompile(`"url":"(https://[^"]+)"`)

// sealLine does what the intake page does with a value: HPKE to the
// request's key in the link's fragment, the line the owner pastes.
func sealLine(t *testing.T, link, value string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	frag, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	id, name := frag.Get("id"), frag.Get("name")
	pub, err := base64.RawURLEncoding.DecodeString(frag.Get("pk"))
	if err != nil {
		t.Fatalf("the link's key: %v", err)
	}
	pk, err := ecdh.P256().NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	hk, err := hpke.NewDHKEMPublicKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := hpke.Seal(hk, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("ganglion-intake-v1\x00"+id+"\x00"+name), []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return "gsec1." + id + "." + base64.RawURLEncoding.EncodeToString(ct)
}

// tablesHolding names every row of every table of db, but the store's own
// table, whose text holds s: where a value must never be.
func tablesHolding(t *testing.T, db *sql.DB, s string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name != 'secrets'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	_ = rows.Close()
	var found []string
	for _, tb := range tables {
		r, err := db.Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if b, ok := v.([]byte); ok && bytes.Contains(b, []byte(s)) || strings.Contains(fmt.Sprint(v), s) {
					found = append(found, tb)
				}
			}
		}
		_ = r.Close()
	}
	return found
}

// The "done when" of #172 and the abuse test of R-INT-7: the agent asks
// for a module's secret with secret.request; the owner opens the card's
// link, the page seals the value, the owner pastes the line; the module
// reads the value with no restart, and the value is in none of the model's
// requests, the database's tables (inbox, outbox, history, journal, turns),
// the logs or the chat, whose pasted line is deleted.
func TestASecretHandedOverFromTheChat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the module tools need git")
	}
	testinputs.Build(t, "../runner/runnertest/probe/wasm", "GOOS=wasip1", "GOARCH=wasm")
	wasm, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	model.CallTool("secret.request", `{"name":"PROBE_TOKEN","why":"the probe reads its token","module":"probe"}`)
	model.Reply("I sent you a card for the probe's token.")
	b := testConfig(t)
	b.BotToken, b.BotAPIURL, b.Admin = testToken, fake.URL(), "telegram:user:42"
	b.ModulesDir, b.LogDir = t.TempDir(), t.TempDir()
	ws := t.TempDir()
	b.Agent = AgentSettings{Models: orModels(t, model.URL(), "m1"), Keys: map[string]string{"or": "sk-test"}, DailyBudgetUSD: 1,
		TZ: time.UTC, Workspace: ws}
	if err := os.MkdirAll(filepath.Join(ws, "modules", "probe"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "modules", "probe", "manifest.yaml"), []byte(tokenManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	logs := &syncWriter{w: &bytes.Buffer{}}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	i := New(ctx, b, logger)
	// The hosted page is this release's copy: no network in a test.
	do.Override(i, func(i do.Injector) (*intake.Registry, error) {
		return intake.New(do.MustInvoke[*access.List](i).IsAdmin, do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			func(context.Context) error { return nil }, intake.WithLogger(logger)), nil
	})
	n := &secretNode{b: b, i: i, reg: nil}
	done := make(chan error, 1)
	go func() {
		done <- node.Run(ctx, []node.Role{node.Core}, func() ([]node.Component, error) { return do.Invoke[[]node.Component](i) }, logger)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("node: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the node did not stop")
		}
		_ = i.Shutdown()
	})
	n.reg = do.MustInvoke[*tool.Registry](i)
	n.install(t, wasm)
	if v, ok := n.token(t, "probe"); ok || v != "" {
		t.Fatalf("before it is set: set %v", ok)
	}

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "set the probe's token"})
	var card, entities string
	defer func() {
		if t.Failed() {
			for _, m := range fake.Sent() {
				t.Logf("sent: %q", m.Text)
			}
			for _, r := range model.Requests() {
				t.Logf("model: %v", r.Messages[len(r.Messages)-1])
			}
		}
	}()
	eventually(t, "the card", func() bool {
		for _, m := range fake.Sent() {
			if strings.Contains(m.Text, "PROBE_TOKEN") && cardLink.MatchString(m.Entities) {
				card, entities = m.Text, m.Entities
				return true
			}
		}
		return false
	})
	if !strings.Contains(card, "probe") || !strings.Contains(card, "the probe reads its token") {
		t.Fatalf("the card does not name the module and quote the reason: %q", card)
	}
	link := strings.ReplaceAll(cardLink.FindStringSubmatch(entities)[1], `\u0026`, "&")
	if !strings.HasPrefix(link, intake.DefaultPage+"#") || !strings.Contains(link, "mode=secret") {
		t.Fatalf("the card links %q", link)
	}

	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: sealLine(t, link, intakeSecret)})
	pasted := fake.LastMessageID()
	eventually(t, "the module to read the value", func() bool {
		v, ok := n.token(t, "probe")
		return ok && v == intakeSecret
	})
	eventually(t, "the card folded", func() bool {
		for _, m := range fake.Showing() {
			if strings.Contains(m.Text, "PROBE_TOKEN") && strings.Contains(m.Text, "is set") {
				return true
			}
		}
		return false
	})
	db := do.MustInvoke[Database](i).DB
	eventually(t, "the node note in the history", func() bool {
		var raw string
		_ = db.QueryRow(`SELECT data FROM conv_state WHERE conv = ?`, "telegram:42").Scan(&raw)
		return strings.Contains(raw, "[secret] PROBE_TOKEN")
	})

	t.Run("the pasted line is deleted from the chat", func(t *testing.T) {
		var gone bool
		for _, d := range fake.Deleted() {
			gone = gone || (d.ChatID == 42 && d.MessageID == pasted)
		}
		if !gone {
			t.Fatalf("deleted %+v, want message %d", fake.Deleted(), pasted)
		}
	})
	t.Run("the value is nowhere but the store", func(t *testing.T) {
		for _, r := range model.Requests() {
			if strings.Contains(fmt.Sprintf("%+v", r), intakeSecret) {
				t.Fatal("a model request holds the value")
			}
		}
		for _, m := range fake.Sent() {
			if strings.Contains(m.Text, intakeSecret) {
				t.Fatal("a sent message holds the value")
			}
		}
		for _, e := range fake.Edits() {
			if strings.Contains(e.Text, intakeSecret) {
				t.Fatal("an edit holds the value")
			}
		}
		if logs.has(intakeSecret) {
			t.Fatal("the log holds the value")
		}
		_ = filepath.WalkDir(b.LogDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(intakeSecret)) {
				t.Errorf("the log file %s holds the value", d.Name())
			}
			return nil
		})
		if found := tablesHolding(t, db, intakeSecret); len(found) > 0 {
			t.Fatalf("the tables %v hold the value", found)
		}
		copied := filepath.Join(t.TempDir(), "node.db")
		if _, err := db.ExecContext(context.Background(), `VACUUM INTO ?`, copied); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(copied)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(intakeSecret)) {
			t.Fatal("the database file holds the value in the clear")
		}
	})
}

// has reports whether what was written holds s.
func (s *syncWriter) has(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.w.String(), sub)
}
