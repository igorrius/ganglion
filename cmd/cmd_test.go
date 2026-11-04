package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
)

// captured runs the command tree with a node runner that records what it
// would start instead of starting it.
func captured(t *testing.T, args ...string) (config.Bootstrap, []node.Role, string, error) {
	t.Helper()
	var (
		out   bytes.Buffer
		b     config.Bootstrap
		roles []node.Role
	)
	start := func(_ context.Context, r []node.Role, got config.Bootstrap, _ *slog.Logger) error {
		b, roles = got, r
		return nil
	}
	err := newRoot(&out, &out, start).Run(context.Background(), append([]string{"ganglion"}, args...))
	return b, roles, out.String(), err
}

// clearEnv unsets every setting, so the developer's own .env does not leak in.
func clearEnv(t *testing.T) {
	for _, k := range []string{"GANGLION_DB", "GANGLION_ADMIN", "TELEGRAM_API_URL", "GANGLION_TELEGRAM_REPLY",
		"GANGLION_METRICS_ADDR", "GANGLION_MODELS_FILE", "GANGLION_WORKSPACE", "GANGLION_DAILY_BUDGET_USD",
		"GANGLION_DAILY_TURNS", "GANGLION_DAILY_TOKENS", "GANGLION_SESSION_IDLE", "GANGLION_TZ", "GANGLION_LOG_LEVEL",
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE", "OPENROUTER_API_KEY", "OPENROUTER_API_KEY_FILE",
		"GANGLION_SEARCH", "BRAVE_API_KEY", "BRAVE_API_KEY_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("GANGLION_LOG_DIR", "off") // no logs/ beside the test's default ganglion.db
}

func TestRunDefaults(t *testing.T) {
	clearEnv(t)
	b, roles, _, err := captured(t, "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 3 || b.DB != "ganglion.db" || b.BotAPIURL != "https://api.telegram.org" || b.BotToken != "" ||
		b.Admin != "" || b.MetricsAddr != "127.0.0.1:9464" || b.TelegramReply != "auto" {
		t.Fatalf("roles %v, bootstrap %+v", roles, b)
	}
	a := b.Agent
	if a.Catalog != nil || a.DailyBudgetUSD != 1.0 || a.TZ.String() != "Europe/Kyiv" || a.Workspace != "workspace" ||
		a.DailyTurns != 300 || a.DailyTokens != 1_000_000 || a.SessionIdle != 6*time.Hour || a.SearchOff || a.BraveKey != "" {
		t.Fatalf("agent defaults %+v", a)
	}
}

const models = `providers:
  - {name: claude, type: claude-cli}
  - {name: openrouter, base_url: https://openrouter.ai/api/v1, key_env: OPENROUTER_API_KEY}
models:
  - {provider: claude, name: haiku, tier: cheap, context: 200000}
  - {provider: openrouter, name: some/model, tier: cheap, tools: true, context: 64000, price_in: 0.3, price_out: 1.2}
`

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every environment variable of the M3a node still sets its setting.
func TestRunReadsEveryVariable(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"GANGLION_DB": "/data/g.db", "GANGLION_ADMIN": "telegram:user:123456", "TELEGRAM_API_URL": "http://127.0.0.1:9",
		"GANGLION_TELEGRAM_REPLY": "always", "GANGLION_METRICS_ADDR": "off",
		"GANGLION_MODELS_FILE": write(t, "models.yaml", models), "GANGLION_WORKSPACE": "/var/lib/ganglion",
		"GANGLION_DAILY_BUDGET_USD": "2.5", "GANGLION_DAILY_TURNS": "100", "GANGLION_DAILY_TOKENS": "500000",
		"GANGLION_SESSION_IDLE": "4h", "GANGLION_TZ": "UTC",
		"TELEGRAM_BOT_TOKEN_FILE": write(t, "tg", "7001:from-file\n"), "OPENROUTER_API_KEY": " sk-or-secret\n",
		"GANGLION_SEARCH": "off", "BRAVE_API_KEY_FILE": write(t, "brave", "BSA-key\n"),
	} {
		t.Setenv(k, v)
	}
	b, _, _, err := captured(t, "run", "--roles", "core")
	if err != nil {
		t.Fatal(err)
	}
	a := b.Agent
	if b.DB != "/data/g.db" || b.Admin != "telegram:user:123456" || b.BotAPIURL != "http://127.0.0.1:9" ||
		b.TelegramReply != "always" || b.MetricsAddr != "off" || b.BotToken != "7001:from-file" {
		t.Fatalf("bootstrap %+v", b)
	}
	if a.Catalog == nil || len(a.Catalog.Models) != 2 || a.Keys["openrouter"] != "sk-or-secret" || a.Keys["claude"] != "" ||
		a.Workspace != "/var/lib/ganglion" || a.DailyBudgetUSD != 2.5 || a.DailyTurns != 100 || a.DailyTokens != 500000 ||
		a.SessionIdle != 4*time.Hour || a.TZ.String() != "UTC" || !a.SearchOff || a.BraveKey != "BSA-key" {
		t.Fatalf("agent %+v", a)
	}
}

func TestFlagsOverrideTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_DB", "/from/env.db")
	b, _, _, err := captured(t, "run", "--db", "/from/flag.db", "--daily-turns", "0", "--session-idle", "off")
	if err != nil || b.DB != "/from/flag.db" || b.Agent.DailyTurns != 0 || b.Agent.SessionIdle != 0 {
		t.Fatalf("%+v, %v", b, err)
	}
}

func TestBadValuesNameTheirVariable(t *testing.T) {
	for k, v := range map[string]string{
		"GANGLION_ADMIN": "@igor", "GANGLION_TELEGRAM_REPLY": "sometimes", "GANGLION_TZ": "Mars/Base",
		"GANGLION_DAILY_BUDGET_USD": "-1", "GANGLION_DAILY_TURNS": "many", "GANGLION_SESSION_IDLE": "soon",
		"GANGLION_LOG_LEVEL": "chatty", "GANGLION_MODELS_FILE": "/missing.yaml", "GANGLION_SEARCH": "google",
	} {
		t.Run(k, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(k, v)
			if _, _, _, err := captured(t, "run"); err == nil || !strings.Contains(err.Error(), k) {
				t.Fatalf("%s=%q: %v, want an error naming the variable", k, v, err)
			}
		})
	}
}

func TestMissingProviderKeyFailsAtStart(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_MODELS_FILE", write(t, "m.yaml", models))
	_, _, _, err := captured(t, "run")
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestSecretErrorsNeverShowTheSecret(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "7001:very-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", write(t, "tg", "7001:very-secret"))
	_, _, _, err := captured(t, "run")
	if err == nil || strings.Contains(err.Error(), "very-secret") || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("both token forms: %v", err)
	}
}

func TestRunRejectsUnknownRole(t *testing.T) {
	clearEnv(t)
	if _, _, _, err := captured(t, "run", "--roles", "core,brain"); err == nil || !strings.Contains(err.Error(), "brain") {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestHelpListsTheSettingsAndTheirVariables(t *testing.T) {
	_, _, out, err := captured(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--db", "GANGLION_DB", "--models-file", "GANGLION_MODELS_FILE", "--tz", "Europe/Kyiv", "ganglion.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("run --help lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"--bot-token", "--telegram-bot-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("a secret is a flag: %s", secret)
		}
	}
}

// Without a command ganglion prints help and fails, so a supervisor that
// still starts the pre-`run` command line sees an error, not a clean exit.
func TestRootWithoutACommandPrintsHelpAndFails(t *testing.T) {
	_, roles, out, err := captured(t)
	if err == nil || !strings.Contains(err.Error(), "ganglion run") || roles != nil ||
		!strings.Contains(out, "run") || !strings.Contains(out, "version") {
		t.Fatalf("roles %v, err %v, out:\n%s", roles, err, out)
	}
}

// Numbers read as decimal, as strconv.Atoi read them: 050 is fifty.
func TestNumbersAreDecimal(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_DAILY_TURNS", "050")
	t.Setenv("GANGLION_DAILY_TOKENS", "0100")
	b, _, _, err := captured(t, "run")
	if err != nil || b.Agent.DailyTurns != 50 || b.Agent.DailyTokens != 100 {
		t.Fatalf("turns %d, tokens %d, %v", b.Agent.DailyTurns, b.Agent.DailyTokens, err)
	}
}

func TestVersionCommand(t *testing.T) {
	_, _, out, err := captured(t, "version")
	if err != nil || !strings.Contains(out, "version: "+buildinfo.Read().Version) || !strings.Contains(out, "go: go1.") {
		t.Fatalf("%v:\n%s", err, out)
	}
	_, _, out, err = captured(t, "version", "--json")
	var in buildinfo.Info
	if err != nil || json.Unmarshal([]byte(out), &in) != nil || in.Version == "" || in.Go == "" {
		t.Fatalf("--json: %v:\n%s", err, out)
	}
	_, _, out, err = captured(t, "--version")
	if err != nil || !strings.Contains(out, buildinfo.Read().Short()) {
		t.Fatalf("--version: %v:\n%s", err, out)
	}
}

// run says which optional parts its settings leave off.
func TestRunAnnouncesWhatIsOff(t *testing.T) {
	clearEnv(t)
	_, _, out, err := captured(t, "run")
	if err != nil || !strings.Contains(out, "the agent is off and the bot echoes") || !strings.Contains(out, "the Telegram transport is off") {
		t.Fatalf("%v:\n%s", err, out)
	}
}

func TestLogDirResolves(t *testing.T) {
	for _, c := range []struct {
		db, flag, dir string
		on            bool
	}{
		{"/data/ganglion.db", "", "/data/logs", true},
		{"ganglion.db", "", "logs", true},
		{"/data/ganglion.db", "off", "", false},
		{"/data/ganglion.db", "/var/log/ganglion", "/var/log/ganglion", true},
	} {
		if dir, on := flags.LogDir(c.db, c.flag); dir != c.dir || on != c.on {
			t.Errorf("LogDir(%q, %q) = %q, %v; want %q, %v", c.db, c.flag, dir, on, c.dir, c.on)
		}
	}
}

// R-OBS-6: by default the logs go to <the database's directory>/logs as
// JSON lines, beside stdout.
func TestRunWritesLogsToDisk(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_LOG_DIR", "")
	dir := t.TempDir()
	b, _, out, err := captured(t, "run", "--db", filepath.Join(dir, "ganglion.db"), "--metrics-addr", "off")
	if err != nil || !strings.Contains(out, "the bot echoes") {
		t.Fatalf("%v:\n%s", err, out)
	}
	if b.Metrics == nil {
		t.Fatal("no metrics renderer reached the node with --metrics-addr off")
	}
	logs, err := os.ReadFile(filepath.Join(dir, "logs", "ganglion.log"))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	first, _, _ := strings.Cut(string(logs), "\n")
	if err := json.Unmarshal([]byte(first), &rec); err != nil || rec["level"] == nil {
		t.Fatalf("not a JSON line: %q (%v)", first, err)
	}
	if !strings.Contains(string(logs), "the bot echoes") {
		t.Fatalf("log file lacks the announce line:\n%s", logs)
	}
}

func TestLogDirOff(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	if _, _, _, err := captured(t, "run", "--db", filepath.Join(dir, "ganglion.db"), "--log-dir", "off"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("logs/ exists with --log-dir off: %v", err)
	}
}
