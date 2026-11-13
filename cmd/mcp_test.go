package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/igorrius/ganglion/settings"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
)

// nodeDB makes a database the way a node leaves it.
func nodeDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ganglion.db")
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlqueue.New(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return path
}

// mcpSession runs `ganglion mcp`, sends the requests on its stdin and
// keeps it open until want answers came, as a client does; it returns the
// answers it wrote to stdout and its stderr.
func mcpSession(t *testing.T, args []string, want int, requests ...string) ([]map[string]any, string) {
	t.Helper()
	inR, inW := io.Pipe()
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- newRootIO(inR, out, errOut, nil).Run(ctx, append([]string{"ganglion", "mcp"}, args...))
	}()
	go func() { _, _ = io.WriteString(inW, strings.Join(requests, "\n")+"\n") }()
	for strings.Count(out.String(), "\n") < want && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("mcp: %v\nstderr: %s", err, errOut.String())
	}
	var answers []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout line is not JSON-RPC: %q", sc.Text())
		}
		answers = append(answers, m)
	}
	// Calls run concurrently: answers come in any order; sort by id.
	slices.SortFunc(answers, func(a, b map[string]any) int {
		x, _ := a["id"].(float64)
		y, _ := b["id"].(float64)
		return int(x - y)
	})
	return answers, errOut.String()
}

const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func TestMCPInitializeAndList(t *testing.T) {
	clearEnv(t)
	db := nodeDB(t)
	answers, _ := mcpSession(t, []string{"--db", db, "--log-dir", "off"}, 3, mcpInit,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"queues_state","arguments":{}}}`)
	if len(answers) != 3 {
		t.Fatalf("answers %v", answers)
	}
	raw, _ := json.Marshal(answers[1])
	for _, name := range []string{"logs_search", "journal_search", "turns_failed", "turn_show", "config_show", "queues_state"} {
		if !strings.Contains(string(raw), `"`+name+`"`) {
			t.Fatalf("tools/list lacks %s: %s", name, raw)
		}
	}
	if call, _ := json.Marshal(answers[2]); !strings.Contains(string(call), `inbox`) || strings.Contains(string(call), `"isError":true`) {
		t.Fatalf("queues_state: %s", call)
	}
	// ADR-0049: what the client's model reads is marked as data, and it is told.
	if init, _ := json.Marshal(answers[0]); !strings.Contains(string(init), `"instructions"`) {
		t.Fatalf("initialize: %s", init)
	}
	if call, _ := json.Marshal(answers[2]); !strings.Contains(string(call), `\u003cexternal source`) && !strings.Contains(string(call), `<external source`) {
		t.Fatalf("queues_state not marked as data: %s", call)
	}
}

// stdout carries JSON-RPC lines only: no log line, no banner.
func TestMCPNoStdoutNoise(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "7001:AAAAbbbbCCCCddddEEEEffffGGGGhhhhIII")
	db := nodeDB(t)
	answers, _ := mcpSession(t, []string{"--db", db}, 2, mcpInit,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"config_show","arguments":{}}}`)
	if len(answers) != 2 {
		t.Fatalf("answers %v", answers)
	}
	raw, _ := json.Marshal(answers[1])
	if strings.Contains(string(raw), "AAAAbbbb") || !strings.Contains(string(raw), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("config_show: %s", raw)
	}
}

// config.show lists the file: secrets only as their variable and whether it
// is set, models by id and the purposes in order.
func TestMCPConfigShowsTheFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-very-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "7001:AAAAbbbbCCCCddddEEEEffffGGGGhhhhIII")
	db := nodeDB(t)
	cfg := writeConfig(t, filepath.Dir(db), quiet+"telegram: {admin: telegram:user:42}\n"+models)
	answers, _ := mcpSession(t, []string{"--db", db, "--config", cfg}, 2, mcpInit,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"config_show","arguments":{}}}`)
	if len(answers) != 2 {
		t.Fatalf("answers %v", answers)
	}
	raw, _ := json.Marshal(answers[1])
	for _, secret := range []string{"sk-or-very-secret", "AAAAbbbb"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("config_show shows a secret: %s", raw)
		}
	}
	for _, want := range []string{"env:OPENROUTER_API_KEY (set)", "env:BRAVE_API_KEY (unset)", "env:TELEGRAM_BOT_TOKEN (set)",
		"telegram:user:42", "chat: haiku (high), some", "some openrouter/some/model"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config_show lacks %q: %s", want, raw)
		}
	}
}

// A store: reference is shown whole with whether it is set, and names no
// variable.
func TestSecretSettingOfAStoreReference(t *testing.T) {
	src := settings.Sources{Env: fakeEnv{"BOT": "from-the-environment"}, Store: fakeEnv{}}
	got := secretSetting("telegram.token", "store:BOT", src)
	if got.Value != "store:BOT (unset)" || got.Env != "" {
		t.Fatalf("an unset store reference = %+v", got)
	}
	src.Store = fakeEnv{"BOT": "t0k"}
	if got := secretSetting("telegram.token", "store:BOT", src); got.Value != "store:BOT (set)" || got.Env != "" {
		t.Fatalf("a set store reference = %+v", got)
	}
	if got := secretSetting("telegram.token", "env:BOT", src); got.Value != "env:BOT (set)" || got.Env != "BOT" {
		t.Fatalf("an env reference = %+v", got)
	}
}

// A config path given with --config must exist, as for run (R-CFG-1):
// config.show says so instead of showing the defaults as the node's.
func TestMCPConfigShowsAMissingNamedFile(t *testing.T) {
	clearEnv(t)
	db := nodeDB(t)
	missing := filepath.Join(filepath.Dir(db), "gangilon.yaml")
	answers, _ := mcpSession(t, []string{"--db", db, "--config", missing}, 2, mcpInit,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"config_show","arguments":{}}}`)
	raw, _ := json.Marshal(answers)
	if !strings.Contains(string(raw), "gangilon.yaml: no such file") {
		t.Fatalf("config_show does not name the missing file: %s", raw)
	}
}

func TestMCPMissingDatabase(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer
	err := newRootIO(strings.NewReader(""), &out, &errOut, nil).Run(context.Background(),
		[]string{"ganglion", "mcp", "--db", filepath.Join(t.TempDir(), "none.db")})
	if err == nil || out.Len() != 0 {
		t.Fatalf("err %v, stdout %q", err, out.String())
	}
}

// A database that is not there is the usual slip: say what to do, not
// SQLite's "unable to open database file (14)".
func TestMCPNamesTheMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	var out bytes.Buffer
	err := mcpCommand(strings.NewReader(""), &out).Run(context.Background(), []string{"mcp", "--db", path})
	if err == nil || !strings.Contains(err.Error(), "no database at "+path) || !strings.Contains(err.Error(), "GANGLION_DB") || strings.Contains(err.Error(), "(14)") {
		t.Fatalf("err %v", err)
	}
}
