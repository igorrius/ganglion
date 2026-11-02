package node

import (
	"errors"
	"github.com/igorrius/ganglion/agent"
	"io/fs"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if v, ok := m[name]; ok {
			return []byte(v), nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := ConfigFromEnv(env(nil), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "ganglion.db" || cfg.BotAPIURL != "https://api.telegram.org" || cfg.BotToken != "" || cfg.Admin != "" {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg, err = ConfigFromEnv(env(map[string]string{
		"GANGLION_DB":        "/data/g.db",
		"TELEGRAM_BOT_TOKEN": "7001:abc",
		"TELEGRAM_API_URL":   "http://127.0.0.1:9",
		"GANGLION_ADMIN":     "telegram:user:123456",
	}), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "/data/g.db" || cfg.BotToken != "7001:abc" || cfg.BotAPIURL != "http://127.0.0.1:9" || cfg.Admin != "telegram:user:123456" {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, bad := range []string{"123456", "telegram:123456", "telegram:user:", "telegram:user:12 34", "@igor"} {
		_, err := ConfigFromEnv(env(map[string]string{"GANGLION_ADMIN": bad}), files(nil))
		if err == nil || !strings.Contains(err.Error(), "GANGLION_ADMIN") || !strings.Contains(err.Error(), "telegram:user:") {
			t.Errorf("GANGLION_ADMIN=%q: %v, want an error naming the variable and the format", bad, err)
		}
	}
}

func TestConfigTokenFromFile(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/tg"}),
		files(map[string]string{"/run/secrets/tg": "7001:from-file\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "7001:from-file" {
		t.Fatalf("token %q, want the trailing newline trimmed", cfg.BotToken)
	}
	_, err = ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/missing"}), files(nil))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN_FILE") || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v, want an error naming the variable", err)
	}
	_, err = ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/empty"}),
		files(map[string]string{"/run/secrets/empty": "\n"}))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestConfigSecretFileContentNotInErrors(t *testing.T) {
	// A file holding junk around the token must not echo it back.
	_, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/f", "GANGLION_ADMIN": "bad"}),
		files(map[string]string{"/f": "7001:very-secret\n"}))
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestConfigTokenBothFormsRejected(t *testing.T) {
	_, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN": "7001:a", "TELEGRAM_BOT_TOKEN_FILE": "/f"}),
		files(map[string]string{"/f": "7001:b"}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") || !strings.Contains(err.Error(), "both") {
		t.Fatalf("both forms: %v", err)
	}
}

func TestConfigTokenWhitespaceTrimmed(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN": " 7001:abc\r\n"}), files(nil))
	if err != nil || cfg.BotToken != "7001:abc" {
		t.Fatalf("token %q, %v; want surrounding whitespace trimmed", cfg.BotToken, err)
	}
}

const models = `
providers:
  - {name: claude, type: claude-cli}
  - {name: openrouter, type: openai, base_url: https://openrouter.ai/api/v1, key_env: OPENROUTER_API_KEY}
models:
  - {provider: claude, name: haiku, tier: cheap, context: 200000}
  - {provider: openrouter, name: some/model, tier: cheap, tools: true, context: 64000, price_in: 0.3, price_out: 1.2}
`

func TestConfigAgent(t *testing.T) {
	cfg, err := ConfigFromEnv(env(nil), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Agent
	if a.Catalog != nil || a.DailyBudgetUSD != 1.0 || a.TZ == nil || a.TZ.String() != "Europe/Kyiv" || a.Workspace != "workspace" {
		t.Fatalf("agent defaults %+v", a)
	}
	cfg, err = ConfigFromEnv(env(map[string]string{
		"GANGLION_MODELS_FILE":      "/etc/ganglion/models.yaml",
		"GANGLION_WORKSPACE":        "/var/lib/ganglion",
		"OPENROUTER_API_KEY_FILE":   "/run/secrets/or",
		"GANGLION_DAILY_BUDGET_USD": "2.5",
		"GANGLION_DAILY_TURNS":      "100",
		"GANGLION_DAILY_TOKENS":     "500000",
		"GANGLION_SESSION_IDLE":     "4h",
		"GANGLION_TZ":               "UTC",
	}), files(map[string]string{
		"/etc/ganglion/models.yaml": models,
		"/run/secrets/or":           "sk-or-secret\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	a = cfg.Agent
	if a.Catalog == nil || len(a.Catalog.Models) != 2 || a.Keys["openrouter"] != "sk-or-secret" || a.Keys["claude"] != "" ||
		a.Workspace != "/var/lib/ganglion" || a.DailyBudgetUSD != 2.5 || a.DailyTurns != 100 || a.DailyTokens != 500000 ||
		a.SessionIdle.Hours() != 4 || a.TZ.String() != "UTC" {
		t.Fatalf("agent config %+v", a)
	}
	for _, bad := range []map[string]string{
		{"GANGLION_TZ": "Mars/Base"},
		{"GANGLION_DAILY_BUDGET_USD": "-1"},
		{"GANGLION_DAILY_BUDGET_USD": "lots"},
		{"GANGLION_DAILY_TURNS": "many"},
		{"GANGLION_SESSION_IDLE": "soon"},
		{"GANGLION_MODELS_FILE": "/missing.yaml"},
	} {
		if _, err := ConfigFromEnv(env(bad), files(nil)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if cfg, err := ConfigFromEnv(env(map[string]string{"GANGLION_DAILY_TURNS": "0", "GANGLION_DAILY_TOKENS": "0"}), files(nil)); err != nil ||
		cfg.Agent.DailyTurns != 0 || cfg.Agent.DailyTokens != 0 {
		t.Errorf("0 must turn the daily limits off (agent's zero): %+v, %v", cfg.Agent, err)
	}
	if cfg, err := ConfigFromEnv(env(map[string]string{"GANGLION_SESSION_IDLE": "off"}), files(nil)); err != nil || cfg.Agent.SessionIdle != 0 {
		t.Errorf("idle off: %v, %v", cfg.Agent.SessionIdle, err)
	}
	def := agent.DefaultConfig()
	if cfg, err := ConfigFromEnv(env(nil), files(nil)); err != nil || cfg.Agent.DailyTurns != def.DailyTurns ||
		cfg.Agent.DailyTokens != def.DailyTokens || cfg.Agent.SessionIdle != def.SessionIdle {
		t.Errorf("unset limits must be the agent's defaults: %+v, %v", cfg.Agent, err)
	}
}

func TestMissingProviderKeyFailsAtStart(t *testing.T) {
	_, err := ConfigFromEnv(env(map[string]string{"GANGLION_MODELS_FILE": "/m.yaml"}), files(map[string]string{"/m.yaml": models}))
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestConfigTelegramReply(t *testing.T) {
	for v, want := range map[string]string{"": "auto", "auto": "auto", "always": "always", "off": "off"} {
		cfg, err := ConfigFromEnv(env(map[string]string{"GANGLION_TELEGRAM_REPLY": v}), files(nil))
		if err != nil || cfg.TelegramReply != want {
			t.Errorf("%q: %q, %v", v, cfg.TelegramReply, err)
		}
	}
	if _, err := ConfigFromEnv(env(map[string]string{"GANGLION_TELEGRAM_REPLY": "sometimes"}), files(nil)); err == nil {
		t.Error("a bad reply mode was accepted")
	}
}

func TestConfigMetricsAddr(t *testing.T) {
	cfg, err := ConfigFromEnv(env(nil), files(nil))
	if err != nil || cfg.MetricsAddr != "127.0.0.1:9464" {
		t.Fatalf("default %q, %v", cfg.MetricsAddr, err)
	}
	cfg, _ = ConfigFromEnv(env(map[string]string{"GANGLION_METRICS_ADDR": "off"}), files(nil))
	if cfg.MetricsAddr != "off" {
		t.Fatalf("off: %q", cfg.MetricsAddr)
	}
}
