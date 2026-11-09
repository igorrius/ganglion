// Package flags declares the command line's flags: each with its
// environment variable, default, check and help category, in one place
// (ADR-0029). Secrets are never flags: they come from the secrets package.
package flags

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // --tz works in images without zoneinfo

	"github.com/urfave/cli/v3"
)

// Help categories.
const (
	catStorage   = "storage"
	catTelegram  = "telegram"
	catAgent     = "agent"
	catTelemetry = "telemetry"
)

// Root holds the flags every command has.
type Root struct {
	LogLevel string
}

// Flags returns the root flags bound to r.
func (r *Root) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "log-level", Usage: "debug, info, warn or error", Value: "info", Category: catTelemetry,
			Sources: env("GANGLION_LOG_LEVEL"), Destination: &r.LogLevel, Validator: in("GANGLION_LOG_LEVEL", validLevel)},
	}
}

// Level is the parsed log level.
func (r *Root) Level() slog.Level {
	var l slog.Level
	_ = l.UnmarshalText([]byte(r.LogLevel)) // checked by the flag's validator
	return l
}

// Run holds the flags of `ganglion run`.
type Run struct {
	Roles            string
	DB               string
	Admin            string
	TelegramAPIURL   string
	TelegramReply    string
	TelegramRich     string
	MetricsAddr      string
	LogDir           string
	ModelsFile       string
	Workspace        string
	SandboxDeny      string
	SandboxHideNames string
	Languages        string
	DailyBudgetUSD   float64
	ReactionAfter    time.Duration
	ReactionDwell    time.Duration
	DailyTurns       int
	DailyTokens      int
	SessionIdle      string
	TZ               string
	Search           string
}

// Flags returns the flags of `ganglion run` bound to r. Defaults that the
// agent owns are passed in, so --help shows them.
func (r *Run) Flags(dailyTurns, dailyTokens int, idle time.Duration) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "roles", Usage: "comma-separated roles to run: core, edge, worker", Value: "core,edge,worker",
			Local: true, Destination: &r.Roles},
		DBFlag(&r.DB),
		&cli.StringFlag{Name: "admin", Usage: "a principal seeded as admin, e.g. telegram:user:123", Category: catTelegram,
			Local: true, Sources: env("GANGLION_ADMIN"), Destination: &r.Admin, Validator: in("GANGLION_ADMIN", validPrincipal)},
		&cli.StringFlag{Name: "telegram-api-url", Usage: "the Bot API server", Value: "https://api.telegram.org", Category: catTelegram,
			Local: true, Sources: env("TELEGRAM_API_URL"), Destination: &r.TelegramAPIURL},
		&cli.StringFlag{Name: "telegram-reply", Usage: "when answers quote the person's message: auto, always or off (ADR-0025)",
			Value: "auto", Category: catTelegram, Local: true, Sources: env("GANGLION_TELEGRAM_REPLY"), Destination: &r.TelegramReply,
			Validator: in("GANGLION_TELEGRAM_REPLY", oneOf("auto", "always", off))},
		&cli.StringFlag{Name: "telegram-rich", Usage: "send a message with a table or two headings as a rich message: on or off (ADR-0054)",
			Value: on, Category: catTelegram, Local: true, Sources: env("GANGLION_TELEGRAM_RICH"), Destination: &r.TelegramRich,
			Validator: in("GANGLION_TELEGRAM_RICH", oneOf(on, off))},
		&cli.DurationFlag{Name: "reaction-after", Usage: "work takes this long before a reaction shows it; 0 = off (ADR-0051)", Value: 8 * time.Second,
			Category: catTelegram, Local: true, Sources: env("GANGLION_REACTION_AFTER"), Destination: &r.ReactionAfter},
		&cli.DurationFlag{Name: "reaction-dwell", Usage: "a reaction stays at least this long before the next", Value: 3 * time.Second,
			Category: catTelegram, Local: true, Sources: env("GANGLION_REACTION_DWELL"), Destination: &r.ReactionDwell},
		&cli.StringFlag{Name: "metrics-addr", Usage: "where /metrics and the dashboard are served, or off (ADR-0026)",
			Value: "127.0.0.1:9464", Category: catTelemetry, Local: true, Sources: env("GANGLION_METRICS_ADDR"), Destination: &r.MetricsAddr},
		LogDirFlag(&r.LogDir),
		&cli.StringFlag{Name: "models-file", Usage: "the model catalogue; without it the agent is off and the bot echoes",
			Category: catAgent, Local: true, Sources: env("GANGLION_MODELS_FILE"), Destination: &r.ModelsFile, TakesFile: true},
		&cli.StringFlag{Name: "languages", Usage: "your language files, <code>.yaml, outside the workspace; default: languages beside the database",
			Category: catAgent, Local: true, Sources: env("GANGLION_LANGUAGES"), Destination: &r.Languages, TakesFile: true},
		&cli.StringFlag{Name: "workspace", Usage: "the agent's files (persona/*.md)", Value: "workspace", Category: catAgent,
			Local: true, Sources: env("GANGLION_WORKSPACE"), Destination: &r.Workspace, TakesFile: true},
		&cli.StringFlag{Name: "sandbox-deny", Usage: "more paths, colon-separated, that commands may neither read nor write",
			Category: catAgent, Local: true, Sources: env("GANGLION_SANDBOX_DENY"), Destination: &r.SandboxDeny},
		&cli.StringFlag{Name: "sandbox-hide-names", Usage: "keep commands from listing the folders that hold a denied path: on or off (ADR-0057)",
			Value: off, Category: catAgent, Local: true, Sources: env("GANGLION_SANDBOX_HIDE_NAMES"), Destination: &r.SandboxHideNames,
			Validator: in("GANGLION_SANDBOX_HIDE_NAMES", oneOf(on, off))},
		&cli.FloatFlag{Name: "daily-budget-usd", Usage: "paid smart models stop for the day after this spend", Value: 1.0,
			Category: catAgent, Local: true, Sources: env("GANGLION_DAILY_BUDGET_USD"), Destination: &r.DailyBudgetUSD,
			Validator: in("GANGLION_DAILY_BUDGET_USD", nonNegative[float64])},
		&cli.IntFlag{Name: "daily-turns", Usage: "turns per person per day; 0 = off", Value: dailyTurns, Category: catAgent, Config: decimal,
			Local: true, Sources: env("GANGLION_DAILY_TURNS"), Destination: &r.DailyTurns, Validator: in("GANGLION_DAILY_TURNS", nonNegative[int])},
		&cli.IntFlag{Name: "daily-tokens", Usage: "tokens per person per day; 0 = off", Value: dailyTokens, Category: catAgent, Config: decimal,
			Local: true, Sources: env("GANGLION_DAILY_TOKENS"), Destination: &r.DailyTokens, Validator: in("GANGLION_DAILY_TOKENS", nonNegative[int])},
		&cli.StringFlag{Name: "session-idle", Usage: "a new session after this much silence, or off", Value: idle.String(),
			Category: catAgent, Local: true, Sources: env("GANGLION_SESSION_IDLE"), Destination: &r.SessionIdle,
			Validator: in("GANGLION_SESSION_IDLE", func(v string) error { _, err := Idle(v); return err })},
		&cli.StringFlag{Name: "tz", Usage: "the time zone of days, limits and the budget", Value: "Europe/Kyiv", Category: catAgent,
			Local: true, Sources: env("GANGLION_TZ"), Destination: &r.TZ,
			Validator: in("GANGLION_TZ", func(v string) error { _, err := time.LoadLocation(v); return err })},
		&cli.StringFlag{Name: "search", Usage: "web.search: auto (Brave with BRAVE_API_KEY, else DuckDuckGo) or off (ADR-0035)",
			Value: "auto", Category: catAgent, Local: true, Sources: env("GANGLION_SEARCH"), Destination: &r.Search,
			Validator: in("GANGLION_SEARCH", oneOf("auto", off))},
	}
}

// on and off are the two values of a setting that is on or off.
const (
	on  = "on"
	off = "off"
)

// decimal reads integers in base 10, as before the flags: 050 is fifty,
// not octal forty.
var decimal = cli.IntegerConfig{Base: 10}

// Idle parses --session-idle: a positive duration, or "off" (0).
func Idle(v string) (time.Duration, error) {
	if v == off {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration like 6h, or off", v)
	}
	return d, nil
}

var principalRE = regexp.MustCompile(`^[a-z][a-z0-9]*:user:[0-9A-Za-z_.-]+$`)

func validPrincipal(v string) error {
	if v != "" && !principalRE.MatchString(v) {
		return fmt.Errorf("%q is not a principal; want <transport>:user:<id>, e.g. telegram:user:123456", v)
	}
	return nil
}

func validLevel(v string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		return fmt.Errorf("%q is not debug, info, warn or error", v)
	}
	return nil
}

func oneOf(allowed ...string) func(string) error {
	return func(v string) error {
		if slices.Contains(allowed, v) {
			return nil
		}
		return fmt.Errorf("%q is not %s", v, strings.Join(allowed, ", "))
	}
}

func nonNegative[T int | float64](v T) error {
	if v < 0 {
		return fmt.Errorf("%v is negative", v)
	}
	return nil
}

// in names the variable in a check's error, so a bad value set in the
// environment points at the variable as well as the flag.
func in[T any](variable string, check func(T) error) func(T) error {
	return func(v T) error {
		if err := check(v); err != nil {
			return fmt.Errorf("%s: %w", variable, err)
		}
		return nil
	}
}

// setEnv is an environment variable that counts only when it is not
// empty, as an unset one did before the flags.
type setEnv string

func env(key string) cli.ValueSourceChain { return cli.NewValueSourceChain(setEnv(key)) }

func (e setEnv) Lookup() (string, bool) {
	v, ok := os.LookupEnv(string(e))
	return v, ok && v != ""
}
func (e setEnv) IsFromEnv() bool  { return true }
func (e setEnv) Key() string      { return string(e) }
func (e setEnv) String() string   { return "environment variable " + strconv.Quote(string(e)) }
func (e setEnv) GoString() string { return "setEnv(" + strconv.Quote(string(e)) + ")" }

// DBFlag is --db, shared by `run` and `mcp`.
func DBFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "db", Usage: "the SQLite database file", Value: "ganglion.db", Category: catStorage,
		Local: true, Sources: env("GANGLION_DB"), Destination: dst, TakesFile: true}
}

// LogDirFlag is --log-dir, shared by `run` and `mcp`: where the log files
// are (ADR-0043).
func LogDirFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "log-dir", Usage: "the log files' directory, or off; default: logs beside the database",
		Category: catTelemetry, Local: true, Sources: env("GANGLION_LOG_DIR"), Destination: dst, TakesFile: true}
}

// LogDir is the log directory for the database at db and the --log-dir
// value v: v itself, "logs" beside the database when v is empty, and
// off (false) for "off".
func LogDir(db, v string) (string, bool) {
	switch v {
	case off:
		return "", false
	case "":
		return filepath.Join(filepath.Dir(db), "logs"), true
	}
	return v, true
}
