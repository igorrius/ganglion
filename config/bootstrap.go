package config

import (
	"time"

	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/tool/builtin"
)

// Bootstrap is what a node needs to start: where its data lives, its
// channels and its agent's settings, with the secrets already read. The
// command line fills it; the providers read it (ADR-0028, ADR-0029).
type Bootstrap struct {
	DB        string // GANGLION_DB, default "ganglion.db"
	Languages string // GANGLION_LANGUAGES: an operator's language files; default "languages" beside the database
	BotToken  string // TELEGRAM_BOT_TOKEN or the file named by TELEGRAM_BOT_TOKEN_FILE; empty → no Telegram transport
	BotAPIURL string // TELEGRAM_API_URL, default "https://api.telegram.org"
	Admin     string // GANGLION_ADMIN, e.g. "telegram:user:123"; seeded as admin at start
	Agent     AgentSettings
	// MetricsAddr is GANGLION_METRICS_ADDR: where /metrics and the promtop
	// dashboard are served (default 127.0.0.1:9464), or "off" (ADR-0026).
	MetricsAddr string
	// TelegramReply is GANGLION_TELEGRAM_REPLY: when answers quote the
	// person's message — auto (default), always or off (ADR-0025).
	TelegramReply string
	// ReactionAfter and ReactionDwell are GANGLION_REACTION_AFTER and
	// GANGLION_REACTION_DWELL: how long work takes before a reaction shows, and
	// how long one stays before the next (ADR-0051). A threshold of 0 is off.
	ReactionAfter, ReactionDwell time.Duration
	// LogDir is where the log files are, from GANGLION_LOG_DIR; "" when
	// logs are not on disk (R-OBS-6). The diagnostic tools search it.
	LogDir string
	// ModulesDir is where module updates are stored: modules/ beside the
	// database (ADR-0041).
	ModulesDir string
	// Metrics draws the node's own metrics for metrics.show and /metrics
	// (ADR-0045); the command line sets it from telemetry, nil leaves the
	// tool out.
	Metrics builtin.MetricsRenderer
}

// AgentSettings turns the agent on. Without a catalogue the core echoes, as
// in M2.
type AgentSettings struct {
	Catalog        *rules.Catalog    // GANGLION_MODELS_FILE; nil = no agent
	Keys           map[string]string // provider name → API key, from each provider's key_env
	Workspace      string            // GANGLION_WORKSPACE, default "workspace": the agent's files (persona/*.md, memory/*.md), a git repository (ADR-0033)
	DailyBudgetUSD float64           // GANGLION_DAILY_BUDGET_USD, default 1.0
	DailyTurns     int               // GANGLION_DAILY_TURNS per person; unset = the agent's default (300), 0 = off
	DailyTokens    int               // GANGLION_DAILY_TOKENS per person; unset = the agent's default (1M), 0 = off
	SessionIdle    time.Duration     // GANGLION_SESSION_IDLE: a duration, or "off" (0); unset = the agent's default (6h)
	TZ             *time.Location    // GANGLION_TZ, default Europe/Kyiv
	BraveKey       string            // BRAVE_API_KEY or the file named by BRAVE_API_KEY_FILE; empty → the key-free search (ADR-0035)
	SearchOff      bool              // GANGLION_SEARCH=off: no web.search tool
}
