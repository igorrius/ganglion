package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

// authorizeLink finds the sign-in link of an OAuth card, in its text or
// its entities.
var authorizeLink = regexp.MustCompile(`https://[^\s"<>)]+/authorize\?[^\s"<>)]+`)

// mcpLeakRig is a core node with a fake MCP server behind a fake OAuth
// authorization server, both trusted by the node's MCP client.
type mcpLeakRig struct {
	fake  *telegramtest.Server
	model *providertest.Server
	mcp   *mcpclienttest.Server
	as    *mcpclienttest.AuthServer
	b     Bootstrap
	i     do.Injector
	logs  *syncWriter
}

func startMCPLeakRig(t *testing.T) *mcpLeakRig {
	t.Helper()
	r := &mcpLeakRig{fake: telegramtest.NewServer(t, testToken), model: providertest.NewServer(t, "sk-test"),
		logs: &syncWriter{w: &bytes.Buffer{}}}
	// Access tokens live a second, so the run refreshes them as it goes.
	r.as = mcpclienttest.NewAuthServer(t, mcpclienttest.AuthOptions{IssParameter: true, ExpiresIn: 1})
	r.mcp = mcpclienttest.NewTLS(t, mcpclienttest.Options{ListenStream: true, Tools: []json.RawMessage{
		mcpclienttest.Tool("search_products", "Search the shop.", "", true),
		mcpclienttest.Tool("add_to_cart", "Add a product to the cart.", "", false),
	}})
	doc, _ := json.Marshal(map[string]any{"resource": r.mcp.URL + "/mcp", "authorization_servers": []string{r.as.URL}})
	r.mcp.Update(func(o *mcpclienttest.Options) {
		o.Bearer = r.as.Valid
		o.Challenge = `Bearer resource_metadata="` + r.mcp.URL + `/.well-known/oauth-protected-resource/mcp"`
		o.Documents = map[string]string{"/.well-known/oauth-protected-resource/mcp": string(doc)}
	})

	r.b = testConfig(t)
	r.b.BotToken, r.b.BotAPIURL, r.b.Admin = testToken, r.fake.URL(), "telegram:user:42"
	r.b.LogDir, r.b.SettingsPath = t.TempDir(), filepath.Join(t.TempDir(), "ganglion.yaml")
	r.b.Agent = AgentSettings{Models: orModels(t, r.model.URL(), "m1"), Keys: map[string]string{"or": "sk-test"},
		DailyBudgetUSD: 1, TZ: time.UTC, Workspace: t.TempDir()}
	logger := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	r.i = New(ctx, r.b, logger)
	// The fakes listen on loopback over httptest's TLS: the node's client
	// trusts their certificate here, and nothing else changes.
	do.Override(r.i, func(do.Injector) (MCPHTTPClient, error) { return MCPHTTPClient{r.mcp.Client()}, nil })
	// The hosted page is this release's copy: no network in a test.
	do.Override(r.i, func(i do.Injector) (*intake.Registry, error) {
		return intake.New(do.MustInvoke[*access.List](i).IsAdmin, do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			func(context.Context) error { return nil }, intake.WithLogger(logger)), nil
	})
	done := make(chan error, 1)
	go func() {
		done <- node.Run(ctx, []node.Role{node.Core}, func() ([]node.Component, error) { return do.Invoke[[]node.Component](r.i) }, logger)
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
		_ = r.i.Shutdown()
	})
	return r
}

// sent waits for a message the bot sent whose text and entities match.
func (r *mcpLeakRig) sent(t *testing.T, what string, match func(telegramtest.Outgoing) bool) telegramtest.Outgoing {
	t.Helper()
	var got telegramtest.Outgoing
	eventually(t, what, func() bool {
		for _, m := range r.fake.Sent() {
			if match(m) {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

// press presses the button labelled label under msg, as the owner.
func (r *mcpLeakRig) press(t *testing.T, msg telegramtest.Outgoing, label string) {
	t.Helper()
	var rows [][]struct {
		Text string `json:"text"`
		Data string `json:"callback_data"`
	}
	if err := json.Unmarshal([]byte(msg.Keyboard), &rows); err != nil {
		t.Fatalf("keyboard %q: %v", msg.Keyboard, err)
	}
	for _, row := range rows {
		for _, b := range row {
			if strings.Contains(b.Text, label) {
				r.fake.AddCallback(telegramtest.Callback{ChatID: 42, UserID: 42, Name: "Igor", MessageID: msg.MessageID, Data: b.Data})
				return
			}
		}
	}
	t.Fatalf("no %q button in %q", label, msg.Keyboard)
}

// state is the hub's state of server.
func (r *mcpLeakRig) state(server string) mcpclient.State {
	for _, s := range do.MustInvoke[*mcpclient.Hub](r.i).States() {
		if s.Name == server {
			return s.State
		}
	}
	return ""
}

// The leak test of R-INT-7 for a sign-in (#261): the model connects a
// server that wants OAuth; the owner signs in and pastes the gauth1 line;
// the node exchanges the code, the owner presses Connect, and a read-only
// tool runs while the access token expires and is refreshed. The code and
// every access and refresh token are then in none of the model's requests,
// the chat, the database's tables but the store's (inbox, outbox, history,
// journal, turns), the database file in the clear or the logs; the pasted
// line is deleted from the chat.
func TestAnOAuthSignInLeavesNoTrace(t *testing.T) {
	r := startMCPLeakRig(t)
	url := r.mcp.URL + "/mcp"
	r.model.CallTool("mcp.connect", `{"name":"shop","url":"`+url+`"}`)
	r.model.Reply("I sent you a card to sign in to shop.")
	defer func() {
		if t.Failed() {
			for _, m := range r.fake.Sent() {
				t.Logf("sent: %q", m.Text)
			}
		}
	}()

	r.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "connect the shop's MCP server"})
	card := r.sent(t, "the sign-in card", func(m telegramtest.Outgoing) bool {
		return authorizeLink.MatchString(unescapeAmp(m.Text + m.Entities))
	})
	link := authorizeLink.FindString(unescapeAmp(card.Text + card.Entities))
	signed, err := r.as.SignIn(link)
	if err != nil {
		t.Fatalf("sign in at %q: %v", link, err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	r.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor",
		Text: "gauth1." + signed.State + "." + b64([]byte(signed.Code)) + "." + b64([]byte(signed.Issuer))})
	pasted := r.fake.LastMessageID()
	tools := r.sent(t, "the tools card", func(m telegramtest.Outgoing) bool {
		return strings.Contains(m.Keyboard, "Connect") && strings.Contains(m.Text, "search_products")
	})
	r.press(t, tools, "Connect")
	eventually(t, "the hub to connect", func() bool { return r.state("shop") == mcpclient.StateConnected })

	// The first access token expires; the model's read-only call runs by
	// itself on a refreshed one.
	time.Sleep(1100 * time.Millisecond)
	r.model.CallTool("mcp.use", `{"server":"shop","tools":["search_products"]}`)
	r.model.CallTool("shop.search_products", `{}`)
	r.model.Reply("Found it.")
	r.fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Name: "Igor", Text: "search the shop"})
	r.sent(t, "the model's answer", func(m telegramtest.Outgoing) bool { return m.Text == "Found it." })
	if r.mcp.Count("tools/call") != 1 {
		t.Fatalf("%d tool calls reached the server", r.mcp.Count("tools/call"))
	}
	refreshed := 0
	for _, q := range r.as.Requests("/token") {
		if q.Form.Get("grant_type") == "refresh_token" {
			refreshed++
		}
	}
	if refreshed == 0 {
		t.Fatal("the token was never refreshed")
	}

	t.Run("the pasted line is deleted from the chat", func(t *testing.T) {
		var gone bool
		for _, d := range r.fake.Deleted() {
			gone = gone || (d.ChatID == 42 && d.MessageID == pasted)
		}
		if !gone {
			t.Fatalf("deleted %+v, want message %d", r.fake.Deleted(), pasted)
		}
	})
	t.Run("the code is nowhere but the store", func(t *testing.T) { r.noTrace(t, signed.Code) })
	for n, v := range r.as.Issued() {
		t.Run(fmt.Sprintf("token %d is nowhere but the store", n+1), func(t *testing.T) { r.noTrace(t, v) })
	}
}

// noTrace checks that v is in no model request, message, edit, log, table
// but the store's, or the database file in the clear.
func (r *mcpLeakRig) noTrace(t *testing.T, v string) {
	t.Helper()
	for _, q := range r.model.Requests() {
		if strings.Contains(fmt.Sprintf("%+v", q), v) {
			t.Error("a model request holds it")
		}
	}
	for _, m := range r.fake.Sent() {
		if strings.Contains(m.Text+m.Entities+m.Keyboard, v) {
			t.Error("a sent message holds it")
		}
	}
	for _, e := range r.fake.Edits() {
		if strings.Contains(e.Text, v) {
			t.Error("an edit holds it")
		}
	}
	if r.logs.has(v) {
		t.Error("the log holds it")
	}
	_ = filepath.WalkDir(r.b.LogDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(v)) {
			t.Errorf("the log file %s holds it", d.Name())
		}
		return nil
	})
	db := do.MustInvoke[Database](r.i).DB
	if found := tablesHolding(t, db, v); len(found) > 0 {
		t.Errorf("the tables %v hold it", found)
	}
	copied := filepath.Join(t.TempDir(), "node.db")
	if _, err := db.ExecContext(context.Background(), `VACUUM INTO ?`, copied); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(copied); err != nil || bytes.Contains(raw, []byte(v)) {
		t.Errorf("the database file holds it in the clear (%v)", err)
	}
}

// unescapeAmp undoes JSON's escape of '&' in entities.
func unescapeAmp(s string) string { return strings.ReplaceAll(s, "\\u0026", "&") }
