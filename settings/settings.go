// Package settings is the node's config file, ganglion.yaml (spec 7.14,
// ADR-0059): everything an operator chooses, with secrets only as env:NAME
// references. Load and Parse are strict: an unknown key or a bad value
// fails with the path in the file.
package settings

import (
	"path/filepath"
	"time"
)

// File is the whole config file. Every section has a default, so a missing
// or empty file is a working node.
type File struct {
	// Node is the process-wide settings.
	Node Node `yaml:"node"`
	// Log is where and how much the node logs.
	Log Log `yaml:"log"`
	// Metrics is the Prometheus endpoint.
	Metrics Metrics `yaml:"metrics"`
	// Telegram is the Telegram transport.
	Telegram Telegram `yaml:"telegram"`
	// Agent is the conversation agent's own settings.
	Agent Agent `yaml:"agent"`
	// Sandbox is what the command sandbox hides and denies.
	Sandbox Sandbox `yaml:"sandbox"`
	// Models chooses the models by purpose; nil means the agent is off.
	Models *Models `yaml:"models"`
	// Modules lists the modules the node runs and their configuration (M4b).
	Modules []Module `yaml:"modules"`
}

// Node is the process-wide settings.
type Node struct {
	// MemoryLimit is the soft memory limit of the Go runtime, such as
	// "400MiB"; 0 takes it from the container's cgroup, else none (R-RUN-6).
	MemoryLimit Size `yaml:"memory_limit"`
	// TZ is the IANA time zone that decides where a day starts.
	TZ string `yaml:"tz"`
}

// Log is the logging settings.
type Log struct {
	// Level is the least severe level written: debug, info, warn or error.
	Level string `yaml:"level"`
	// Dir is the directory of the rotated log files; "" puts them beside the
	// database and "off" writes no files.
	Dir string `yaml:"dir"`
}

// Metrics is the Prometheus endpoint.
type Metrics struct {
	// Addr is the listen address host:port, or "off" for no endpoint.
	Addr string `yaml:"addr"`
}

// Telegram is the Telegram transport's settings.
type Telegram struct {
	// Token is the bot token, as env:NAME.
	Token SecretRef `yaml:"token"`
	// APIURL is the Bot API's base address.
	APIURL string `yaml:"api_url"`
	// Admin is the principal that is admin from the first start, as
	// telegram:user:<id>; "" seeds none.
	Admin string `yaml:"admin"`
	// Reply is when the bot answers in a group: auto, always or off.
	Reply string `yaml:"reply"`
	// Rich turns Markdown into Telegram formatting.
	Rich bool `yaml:"rich"`
	// Reactions is the bot's reaction to a message while it works.
	Reactions Reactions `yaml:"reactions"`
}

// Reactions is how the bot shows that it has seen a message.
type Reactions struct {
	// After is how long a turn runs before the first reaction; off is 0.
	After Duration `yaml:"after"`
	// Dwell is how long a reaction stays before the next one.
	Dwell Duration `yaml:"dwell"`
}

// Agent is the conversation agent's settings.
type Agent struct {
	// Workspace is the directory of the agent's files.
	Workspace string `yaml:"workspace"`
	// Languages is the directory of language files; "" puts them beside the
	// database.
	Languages string `yaml:"languages"`
	// SessionIdle is the silence after which a conversation starts afresh;
	// off is 0.
	SessionIdle Duration `yaml:"session_idle"`
	// Limits caps what one day of conversation may use.
	Limits Limits `yaml:"limits"`
	// Search is the web search the agent may use.
	Search Search `yaml:"search"`
}

// Limits is the daily caps; 0 or off means no cap. The scalar off turns all
// of them off.
type Limits struct {
	// DailyBudgetUSD is the most US dollars of model cost a day.
	DailyBudgetUSD Money `yaml:"daily_budget_usd"`
	// DailyTurns is the most turns a day.
	DailyTurns Count `yaml:"daily_turns"`
	// DailyTokens is the most tokens a day.
	DailyTokens Count `yaml:"daily_tokens"`
}

// Search is the web search settings.
type Search struct {
	// Mode is auto (use a search key when there is one) or off.
	Mode string `yaml:"mode"`
	// BraveKey is the Brave Search API key, as env:NAME.
	BraveKey SecretRef `yaml:"brave_key"`
}

// Sandbox is what the command sandbox hides from the agent.
type Sandbox struct {
	// Deny lists extra paths the sandbox refuses.
	Deny []string `yaml:"deny"`
	// HideNames hides the names of denied paths too.
	HideNames bool `yaml:"hide_names"`
}

// Module is one module the node runs (M4b).
type Module struct {
	// Name is the module's name.
	Name string `yaml:"name"`
	// Config is the module's own settings, checked against its manifest.
	Config map[string]any `yaml:"config"`
	// Off keeps the module installed but not running.
	Off bool `yaml:"off"`
}

// Default is the file a node runs with when it has none: the defaults the
// flags had before M4.
func Default() File {
	return File{
		Node:    Node{TZ: "Europe/Kyiv"},
		Log:     Log{Level: "info"},
		Metrics: Metrics{Addr: "127.0.0.1:9464"},
		Telegram: Telegram{
			Token:     "env:TELEGRAM_BOT_TOKEN",
			APIURL:    "https://api.telegram.org",
			Reply:     auto,
			Rich:      true,
			Reactions: Reactions{After: Duration(8 * time.Second), Dwell: Duration(3 * time.Second)},
		},
		Agent: Agent{
			Workspace:   "workspace",
			SessionIdle: Duration(6 * time.Hour),
			Limits:      Limits{DailyBudgetUSD: 1.0, DailyTurns: 300, DailyTokens: 1_000_000},
			Search:      Search{Mode: auto, BraveKey: "env:BRAVE_API_KEY"},
		},
	}
}

// LogDir is the log directory of a node whose database is at db: log.dir
// itself, "logs" beside the database when it is empty, and off (false) for
// "off" (R-OBS-6).
func (f File) LogDir(db string) (string, bool) {
	switch f.Log.Dir {
	case off:
		return "", false
	case "":
		return filepath.Join(filepath.Dir(db), "logs"), true
	}
	return f.Log.Dir, true
}
