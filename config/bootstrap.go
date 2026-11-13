package config

import (
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/internal/memlimit"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool/builtin"
)

// Bootstrap is what a node needs to start: where its data lives, its
// channels and its agent's settings, with the secrets already read. The
// command line fills it from ganglion.yaml (ADR-0059); the providers read
// it (ADR-0028, ADR-0029).
type Bootstrap struct {
	DB        string // --db (GANGLION_DB), default "ganglion.db"
	Languages string // agent.languages: an operator's language files; default "languages" beside the database
	BotToken  string // the variable telegram.token names, or its _FILE; empty → no Telegram transport
	BotAPIURL string // telegram.api_url, default "https://api.telegram.org"
	Admin     string // telegram.admin, e.g. "telegram:user:123"; seeded as admin at start
	Agent     AgentSettings
	// Timeouts bound a turn, its model calls and the node's handler
	// (agent.call_timeout, agent.turn_timeout; ruling 17 of M4c).
	Timeouts Timeouts
	// MetricsAddr is metrics.addr: where /metrics and the promtop
	// dashboard are served (default 127.0.0.1:9464), or "off" (ADR-0026).
	MetricsAddr string
	// HTTPAddr is http.addr: where the public listener listens, or "off"
	// (R-CFG-11). PublicURL is http.public_url, the address the outside
	// reaches it at.
	HTTPAddr, PublicURL string
	// TelegramWebhook is telegram.updates: webhook — Telegram calls the
	// public listener instead of being polled (R-DLV-18).
	TelegramWebhook bool
	// TelegramReply is telegram.reply: when answers quote the person's
	// message — auto (default), always or off (ADR-0025).
	TelegramReply string
	// TelegramRich is telegram.rich: a message with a table or two
	// headings goes out as a rich message (ADR-0054).
	TelegramRich bool
	// ReactionAfter and ReactionDwell are telegram.reactions.after and
	// .dwell: how long work takes before a reaction shows, and how long one
	// stays before the next (ADR-0051). A threshold of 0 is off.
	ReactionAfter, ReactionDwell time.Duration
	// LogDir is where the log files are, from log.dir; "" when logs are
	// not on disk (R-OBS-6). The diagnostic tools search it.
	LogDir string
	// ModulesDir is where module updates are stored: modules/ beside the
	// database (ADR-0041).
	ModulesDir string
	// ClaudeDir is where the claude CLI runs: claude/ beside the database
	// (R-CMD-8). Empty keeps a private temporary folder.
	ClaudeDir string
	// SandboxDeny is sandbox.deny: paths the owner adds to what commands
	// may neither read nor write (R-CMD-3).
	SandboxDeny []string
	// SandboxHideNames is sandbox.hide_names: commands may not list the
	// folders the sandbox splits, so the names inside denied folders stay
	// hidden too (ADR-0057). Off by default.
	SandboxHideNames bool
	// Build is what the module builder is told: where cargo and the
	// toolchain are, and its limits (R-RUN-9). The command line fills it from
	// the node's environment; Cargo "" means this node builds nothing.
	Build builder.LocalConfig
	// MemoryLimit is the Go runtime's soft memory limit the command line
	// set, and where it came from (R-RUN-6).
	MemoryLimit memlimit.Limit
	// Settings is the file the node read, for config.show and the parts
	// that edit it (M4b).
	Settings settings.File
	// SettingsPath is where that file is (or would be: a node may start
	// without one), which the parts that edit its modules: section write;
	// "" is ganglion.yaml beside the database.
	SettingsPath string
	// Examples is the folder of example modules module.import copies from;
	// "" finds the image's /app/examples/modules or examples/modules where
	// the node runs (a checkout).
	Examples string
	// Secrets is where the node reads secrets that are not in the file, such
	// as those a module's manifest names (R-RUN-1); nil reads none.
	Secrets secrets.Source
	// Metrics draws the node's own metrics for metrics.show and /metrics
	// (ADR-0045); the command line sets it from telemetry, nil leaves the
	// tool out.
	Metrics builtin.MetricsRenderer
}

// SecretValues are the secret values the node read, which logs and command
// output hide exactly (redact.Values).
func (b Bootstrap) SecretValues() []string {
	out := []string{b.BotToken, b.Agent.BraveKey}
	for _, k := range b.Agent.Keys {
		out = append(out, k)
	}
	return out
}

// Timeouts are how long the agent's work may take: the settings check that
// the handler's stays below the queue's lease.
type Timeouts struct {
	Call    time.Duration // agent.call_timeout, default 3m: one model call
	Turn    time.Duration // agent.turn_timeout, default 5m: a whole turn
	Handler time.Duration // the turn's plus settings.HandlerSlack: the loop's limit on the node's handler
}

// AgentSettings turns the agent on. Without models the core echoes, as in
// M2.
type AgentSettings struct {
	Models         *settings.Models  // the models: section; nil = no agent
	Keys           map[string]string // provider name → API key, from the variable each provider's key names
	Workspace      string            // agent.workspace, default "workspace": the agent's files (persona/*.md, memory/*.md), a git repository (ADR-0033)
	DailyBudgetUSD float64           // agent.limits.daily_budget_usd, default 1.0
	DailyTurns     int               // agent.limits.daily_turns per person, default 300; 0 = off
	DailyTokens    int               // agent.limits.daily_tokens per person, default 1M; 0 = off
	SessionIdle    time.Duration     // agent.session_idle, default 6h; 0 = off
	TZ             *time.Location    // node.tz, default Europe/Kyiv
	BraveKey       string            // the variable agent.search.brave_key names, or its _FILE; empty → the key-free search (ADR-0035)
	SearchOff      bool              // agent.search.mode off: no web.search tool
	Memory         MemorySettings    // the memory: section (M5)
}

// MemorySettings is the memory: section (R-CFG-8).
type MemorySettings struct {
	PromptBudget int           // memory.prompt_budget, default 24000 characters; 0 folds every topic (R-MEM-7)
	Review       bool          // memory.review.enabled, default on (R-MEM-8)
	Quiet        time.Duration // memory.review.quiet, default 10m
}

// DefaultMemory is the memory: section's defaults, for a bootstrap made in
// code (the lab, tests).
func DefaultMemory() MemorySettings {
	return MemorySettings{PromptBudget: 24000, Review: true, Quiet: 10 * time.Minute}
}
