package node

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata" // GANGLION_TZ works in images without zoneinfo

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/secrets"
)

// Config is the node configuration. Until D-3 (YAML node config, M4) it
// comes from environment variables.
type Config struct {
	DB        string // GANGLION_DB, default "ganglion.db"
	BotToken  string // TELEGRAM_BOT_TOKEN or the file named by TELEGRAM_BOT_TOKEN_FILE; empty → no Telegram transport
	BotAPIURL string // TELEGRAM_API_URL, default "https://api.telegram.org"
	Admin     string // GANGLION_ADMIN, e.g. "telegram:user:123"; seeded as admin at start
	Agent     AgentConfig
	// MetricsAddr is GANGLION_METRICS_ADDR: where /metrics and the promtop
	// dashboard are served (default 127.0.0.1:9464), or "off" (ADR-0026).
	MetricsAddr string
	// TelegramReply is GANGLION_TELEGRAM_REPLY: when answers quote the
	// person's message — auto (default), always or off (ADR-0025).
	TelegramReply string
}

// AgentConfig turns the agent on. Without a catalogue the core echoes, as
// in M2.
type AgentConfig struct {
	Catalog        *rules.Catalog    // GANGLION_MODELS_FILE; nil = no agent
	Keys           map[string]string // provider name → API key, from each provider's key_env
	Workspace      string            // GANGLION_WORKSPACE, default "workspace": the agent's files (persona/*.md; memory in M3b)
	DailyBudgetUSD float64           // GANGLION_DAILY_BUDGET_USD, default 1.0
	DailyTurns     int               // GANGLION_DAILY_TURNS per person; unset = the agent's default (300), 0 = off
	DailyTokens    int               // GANGLION_DAILY_TOKENS per person; unset = the agent's default (1M), 0 = off
	SessionIdle    time.Duration     // GANGLION_SESSION_IDLE: a duration, or "off" (0); unset = the agent's default (6h)
	TZ             *time.Location    // GANGLION_TZ, default Europe/Kyiv
}

var principalRE = regexp.MustCompile(`^[a-z][a-z0-9]*:user:[0-9A-Za-z_.-]+$`)

// ConfigFromEnv reads the configuration through getenv; readFile reads
// secret files (os.ReadFile).
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Config{
		DB:        getenv("GANGLION_DB"),
		BotAPIURL: getenv("TELEGRAM_API_URL"),
		Admin:     getenv("GANGLION_ADMIN"),
	}
	if cfg.DB == "" {
		cfg.DB = "ganglion.db"
	}
	if cfg.BotAPIURL == "" {
		cfg.BotAPIURL = "https://api.telegram.org"
	}
	if cfg.Admin != "" && !principalRE.MatchString(cfg.Admin) {
		return Config{}, fmt.Errorf("GANGLION_ADMIN=%q is not a principal; want <transport>:user:<id>, e.g. telegram:user:123456", cfg.Admin)
	}
	src := secrets.Env{Getenv: getenv, ReadFile: readFile}
	var err error
	if cfg.BotToken, err = src.Secret("TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.MetricsAddr = getenv("GANGLION_METRICS_ADDR"); cfg.MetricsAddr == "" {
		cfg.MetricsAddr = "127.0.0.1:9464"
	}
	switch cfg.TelegramReply = getenv("GANGLION_TELEGRAM_REPLY"); cfg.TelegramReply {
	case "":
		cfg.TelegramReply = "auto"
	case "auto", "always", "off":
	default:
		return Config{}, fmt.Errorf("GANGLION_TELEGRAM_REPLY=%q is not auto, always or off", cfg.TelegramReply)
	}
	if cfg.Agent, err = agentFromEnv(getenv, readFile); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func agentFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (AgentConfig, error) {
	def := agent.DefaultConfig()
	a := AgentConfig{DailyBudgetUSD: 1.0, DailyTurns: def.DailyTurns, DailyTokens: def.DailyTokens, SessionIdle: def.SessionIdle}
	tz := getenv("GANGLION_TZ")
	if tz == "" {
		tz = "Europe/Kyiv"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return a, fmt.Errorf("GANGLION_TZ=%q: %w", tz, err)
	}
	a.TZ = loc
	if v := getenv("GANGLION_DAILY_BUDGET_USD"); v != "" {
		if a.DailyBudgetUSD, err = strconv.ParseFloat(v, 64); err != nil || a.DailyBudgetUSD < 0 {
			return a, fmt.Errorf("GANGLION_DAILY_BUDGET_USD=%q is not a non-negative number", v)
		}
	}
	for name, dst := range map[string]*int{"GANGLION_DAILY_TURNS": &a.DailyTurns, "GANGLION_DAILY_TOKENS": &a.DailyTokens} {
		if v := getenv(name); v != "" {
			if *dst, err = strconv.Atoi(v); err != nil || *dst < 0 {
				return a, fmt.Errorf("%s=%q is not a whole number (0 turns the limit off)", name, v)
			}
		}
	}
	switch v := getenv("GANGLION_SESSION_IDLE"); v {
	case "":
	case "off":
		a.SessionIdle = 0
	default:
		if a.SessionIdle, err = time.ParseDuration(v); err != nil || a.SessionIdle <= 0 {
			return a, fmt.Errorf("GANGLION_SESSION_IDLE=%q is not a positive duration like 6h, or off", v)
		}
	}
	if a.Workspace = getenv("GANGLION_WORKSPACE"); a.Workspace == "" {
		a.Workspace = "workspace"
	}
	f := getenv("GANGLION_MODELS_FILE")
	if f == "" {
		return a, nil
	}
	b, err := readFile(f)
	if err != nil {
		return a, fmt.Errorf("GANGLION_MODELS_FILE: %w", err)
	}
	cat, err := rules.LoadCatalog(bytes.NewReader(b))
	if err != nil {
		return a, fmt.Errorf("GANGLION_MODELS_FILE %s: %w", f, err)
	}
	a.Catalog, a.Keys = &cat, map[string]string{}
	for _, p := range cat.Providers {
		if p.KeyEnv == "" {
			continue
		}
		key, err := secrets.Env{Getenv: getenv, ReadFile: readFile}.Secret(p.KeyEnv)
		if err != nil {
			return a, err
		}
		if key == "" {
			return a, fmt.Errorf("%s (key_env of provider %s) is not set; set it or %s_FILE", p.KeyEnv, p.Name, p.KeyEnv)
		}
		a.Keys[p.Name] = key
	}
	return a, nil
}
