// Package settings is the node's config file, ganglion.yaml (spec 7.14,
// ADR-0059): everything an operator chooses, with secrets only as env:NAME or store:NAME
// references. Load and Parse are strict: an unknown key or a bad value
// fails with the path in the file.
package settings

import (
	"path/filepath"
	"regexp"
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
	// HTTP is the node's public listener (R-CFG-11).
	HTTP HTTP `yaml:"http"`
	// Telegram is the Telegram transport.
	Telegram Telegram `yaml:"telegram"`
	// Agent is the conversation agent's own settings.
	Agent Agent `yaml:"agent"`
	// Sandbox is what the command sandbox hides and denies.
	Sandbox Sandbox `yaml:"sandbox"`
	// Models chooses the models by purpose; nil means the agent is off.
	Models *Models `yaml:"models"`
	// Destinations names the conversations a message may be sent to
	// besides the one it answers (R-CFG-12).
	Destinations []Destination `yaml:"destinations"`
	// Modules lists the modules the node runs and their configuration (M4b).
	Modules []Module `yaml:"modules"`
	// Memory is the agent's long-term memory (M5, R-CFG-8).
	Memory Memory `yaml:"memory"`
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

// HTTP is the node's public listener: the one entry the outside world has
// to the node (R-CFG-11, ADR-0078). It serves plain HTTP behind a proxy
// that ends TLS.
type HTTP struct {
	// Addr is the listen address host:port, or "off" for no listener.
	Addr string `yaml:"addr"`
	// PublicURL is the HTTPS address the outside reaches the listener at;
	// "" when nobody was told one.
	PublicURL string `yaml:"public_url"`
}

// On reports whether the node runs the public listener: a file made in
// code that names no address runs none.
func (h HTTP) On() bool { return h.Addr != off && h.Addr != "" }

// The ways Telegram updates reach the node (telegram.updates, R-DLV-18).
const (
	UpdatesPolling = "polling" // the node asks Telegram (getUpdates)
	UpdatesWebhook = "webhook" // Telegram calls the node's public listener
)

// Telegram is the Telegram transport's settings.
type Telegram struct {
	// Token is the bot token, as env:NAME or store:NAME.
	Token SecretRef `yaml:"token"`
	// APIURL is the Bot API's base address.
	APIURL string `yaml:"api_url"`
	// Admin is the principal that is admin from the first start, as
	// telegram:user:<id>; "" seeds none.
	Admin string `yaml:"admin"`
	// Updates is how updates arrive: polling or webhook (R-DLV-18).
	Updates string `yaml:"updates"`
	// Reply is when the bot answers in a group: auto, always or off.
	Reply string `yaml:"reply"`
	// Rich turns Markdown into Telegram formatting.
	Rich bool `yaml:"rich"`
	// Reactions is the bot's reaction to a message while it works.
	Reactions Reactions `yaml:"reactions"`
	// Groups are the group chats whose members may talk to the bot
	// (R-DLV-17). The node writes a line when the admin allows a group.
	Groups []Group `yaml:"groups"`
}

// GroupRoleFamily is the one role a group's line may name: allowed and not
// an admin. More roles are the spec's "Not now".
const GroupRoleFamily = "family"

// Group is one line of telegram.groups.
type Group struct {
	// Chat is Telegram's id of the group, a negative number.
	Chat int64 `yaml:"chat"`
	// Name is for people: the group's title when it was connected.
	Name string `yaml:"name"`
	// Role is what the group's members may do: family.
	Role string `yaml:"role"`
}

// check says what is wrong with the line: the key and the reason; "" when
// nothing is.
func (g Group) check() (key, reason string) {
	if g.Chat >= 0 {
		return "chat", "must be a group's id, a negative number"
	}
	if g.Role != GroupRoleFamily {
		return "role", "must be family"
	}
	return "", ""
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
	// CallTimeout bounds one model call. On the claude CLI route one call
	// runs every tool call of its turn (ruling 17 of M4c).
	CallTimeout Duration `yaml:"call_timeout"`
	// TurnTimeout bounds a whole turn; the node's handler gets HandlerSlack
	// more, which must stay below QueueLease.
	TurnTimeout Duration `yaml:"turn_timeout"`
	// Limits caps what one day of conversation may use.
	Limits Limits `yaml:"limits"`
	// Search is the web search the agent may use.
	Search Search `yaml:"search"`
}

// HandlerSlack is how much longer than a turn the node's handler may run: it
// stores the turn's outcome after the turn's own deadline (ruling 17 of M4c).
const HandlerSlack = time.Minute

// QueueLease is how long a turn holds its event before the queue offers it
// again (core.DefaultLease): the handler must end before it.
const QueueLease = 10 * time.Minute

// HandlerTimeout is the node handler's limit for a turn: the turn's plus
// HandlerSlack.
func (a Agent) HandlerTimeout() time.Duration { return time.Duration(a.TurnTimeout) + HandlerSlack }

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
	// BraveKey is the Brave Search API key, as env:NAME or store:NAME.
	BraveKey SecretRef `yaml:"brave_key"`
}

// Sandbox is what the command sandbox hides from the agent.
type Sandbox struct {
	// Deny lists extra paths the sandbox refuses.
	Deny []string `yaml:"deny"`
	// HideNames hides the names of denied paths too.
	HideNames bool `yaml:"hide_names"`
}

// Memory is the agent's long-term memory (R-CFG-8).
type Memory struct {
	// PromptBudget is the size in characters of the known-facts block of
	// the system prompt; over it, topics fold (R-MEM-7).
	PromptBudget Count `yaml:"prompt_budget"`
	// Review is the pass that reads a quiet conversation for facts the turn
	// missed (R-MEM-8).
	Review Review `yaml:"review"`
}

// Review is the memory review's settings.
type Review struct {
	// Enabled runs the review.
	Enabled bool `yaml:"enabled"`
	// Quiet is how long a conversation is silent before it is reviewed.
	Quiet Duration `yaml:"quiet"`
}

// Module is one module the node runs (M4b).
type Module struct {
	// Name is the module's name.
	Name string `yaml:"name"`
	// Config is the module's own settings, checked against its manifest.
	Config map[string]any `yaml:"config"`
	// Off keeps the module installed but not running.
	Off bool `yaml:"off"`
	// NotifyTo names the destinations the module's messages go to, each
	// DestinationOwner or a name under destinations; missing means the
	// owner alone (R-RUN-18).
	NotifyTo []string `yaml:"notify_to"`
}

// DestinationOwner is the built-in destination: the private chat of a
// module's owner.
const DestinationOwner = "owner"

// MaxNotifyTo is how many destinations a module's line may name.
const MaxNotifyTo = 8

// Destination is one line of destinations: a name for a conversation a
// message may be sent to (R-CFG-12).
type Destination struct {
	// Name is how the admin and a module's line call it.
	Name string `yaml:"name"`
	// Conv is the conversation's key, such as telegram:-1001234567890/45.
	Conv string `yaml:"conv"`
}

var (
	destinationName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	destinationConv = regexp.MustCompile(`^[a-z][a-z0-9]*:-?[0-9]+(/[0-9]+)?$`)
)

// check says what is wrong with the line: the key and the reason; "" when
// nothing is.
func (d Destination) check() (key, reason string) {
	if !destinationName.MatchString(d.Name) {
		return "name", "must be lower-case letters, digits and dashes, starting with a letter, at most 32"
	}
	if d.Name == DestinationOwner {
		return "name", "must not be owner: that name is the module owner's private chat"
	}
	if !destinationConv.MatchString(d.Conv) {
		return "conv", "must be a conversation's key, such as telegram:-1001234567890/45"
	}
	return "", ""
}

// Default is the file a node runs with when it has none: the defaults the
// flags had before M4.
func Default() File {
	return File{
		Node:    Node{TZ: "Europe/Kyiv"},
		Log:     Log{Level: "info"},
		Metrics: Metrics{Addr: "127.0.0.1:9464"},
		HTTP:    HTTP{Addr: off},
		Telegram: Telegram{
			Token:     "env:TELEGRAM_BOT_TOKEN",
			APIURL:    "https://api.telegram.org",
			Updates:   UpdatesPolling,
			Reply:     auto,
			Rich:      true,
			Reactions: Reactions{After: Duration(8 * time.Second), Dwell: Duration(3 * time.Second)},
		},
		Agent: Agent{
			Workspace:   "workspace",
			SessionIdle: Duration(6 * time.Hour),
			CallTimeout: Duration(3 * time.Minute),
			TurnTimeout: Duration(5 * time.Minute),
			Limits:      Limits{DailyBudgetUSD: 1.0, DailyTurns: 300, DailyTokens: 1_000_000},
			Search:      Search{Mode: auto, BraveKey: "env:BRAVE_API_KEY"},
		},
		Memory: Memory{PromptBudget: 24000, Review: Review{Enabled: true, Quiet: Duration(10 * time.Minute)}},
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

// SecretNames are the variables every env: reference of the file names:
// the bot token, the search key and the providers' keys. A skill script
// never gets them (R-RTR-5, ADR-0042). A store: reference is not a
// variable and is not listed.
func (f File) SecretNames() []string { return f.refNames(SchemeEnv) }

// StoreNames are the names every store: reference of the file names.
func (f File) StoreNames() []string { return f.refNames(SchemeStore) }

// refNames are the names of the file's references of one scheme.
func (f File) refNames(scheme Scheme) []string {
	refs := []SecretRef{f.Telegram.Token, f.Agent.Search.BraveKey}
	if f.Models != nil {
		refs = append(refs, f.Models.keyRefs()...)
	}
	var out []string
	for _, r := range refs {
		if r.Scheme() == scheme {
			out = append(out, r.Name())
		}
	}
	return out
}
