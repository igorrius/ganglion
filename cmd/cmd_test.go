package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/sandbox"
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

// settingVars are the config path, the database path, the secrets the tests
// use and the retired variables: what a developer's own .env may set.
var settingVars = []string{"GANGLION_CONFIG", "GANGLION_DB", "GANGLION_ADMIN", "TELEGRAM_API_URL", "GANGLION_TELEGRAM_REPLY",
	"GANGLION_TELEGRAM_RICH", "GANGLION_REACTION_AFTER", "GANGLION_REACTION_DWELL", "GANGLION_METRICS_ADDR", "GANGLION_LOG_DIR",
	"GANGLION_MODELS_FILE", "GANGLION_LANGUAGES", "GANGLION_WORKSPACE", "GANGLION_DAILY_BUDGET_USD", "GANGLION_DAILY_TURNS",
	"GANGLION_DAILY_TOKENS", "GANGLION_SESSION_IDLE", "GANGLION_TZ", "GANGLION_LOG_LEVEL", "GANGLION_SEARCH",
	"GANGLION_SANDBOX_DENY", "GANGLION_SANDBOX_HIDE_NAMES",
	"TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE", "OPENROUTER_API_KEY", "OPENROUTER_API_KEY_FILE",
	"BRAVE_API_KEY", "BRAVE_API_KEY_FILE"}

// clearEnv unsets settingVars, so the developer's own .env does not leak in.
func clearEnv(t *testing.T) {
	for _, k := range settingVars {
		t.Setenv(k, "")
	}
}

// childEnv is the environment of a node the test starts as a process:
// this one's with settingVars empty, then extra.
func childEnv(extra ...string) []string {
	env := os.Environ()
	for _, k := range settingVars {
		env = append(env, k+"=")
	}
	return append(env, extra...)
}

// quiet is the part of a test's config that keeps it off the network and
// the disk: no metrics endpoint, no log files.
const quiet = "metrics: {addr: off}\nlog: {dir: off}\n"

// nodeFiles writes body as ganglion.yaml into a new directory and returns
// the arguments that run a node on it with its database beside it.
func nodeFiles(t *testing.T, body string) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{"--db", filepath.Join(dir, "ganglion.db"), "--config", writeConfig(t, dir, body)}
}

// writeConfig writes ganglion.yaml into dir and returns its path.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "ganglion.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A node without a file runs on the defaults: an echo bot, as a node
// without variables did before M4.
func TestRunDefaults(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	b, roles, _, err := captured(t, "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 3 || b.DB != "ganglion.db" || b.BotAPIURL != "https://api.telegram.org" || b.BotToken != "" ||
		b.Admin != "" || b.MetricsAddr != "127.0.0.1:9464" || b.TelegramReply != "auto" || !b.TelegramRich || b.SandboxHideNames ||
		b.LogDir != "logs" || b.ReactionAfter != 8*time.Second || b.ReactionDwell != 3*time.Second {
		t.Fatalf("roles %v, bootstrap %+v", roles, b)
	}
	a := b.Agent
	if a.Models != nil || a.DailyBudgetUSD != 1.0 || a.TZ.String() != "Europe/Kyiv" || a.Workspace != "workspace" ||
		a.DailyTurns != 300 || a.DailyTokens != 1_000_000 || a.SessionIdle != 6*time.Hour || a.SearchOff || a.BraveKey != "" {
		t.Fatalf("agent defaults %+v", a)
	}
}

const models = `models:
  providers:
    - {name: claude, type: claude-cli}
    - {name: openrouter, base_url: https://openrouter.ai/api/v1, key: env:OPENROUTER_API_KEY}
  models:
    - {provider: claude, name: haiku, context: 200000}
    - {id: some, provider: openrouter, name: some/model, tools: true, context: 64000, price_in: 0.3, price_out: 1.2}
  purposes:
    chat: [{model: haiku, effort: high}, {model: some}]
`

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every setting of the file reaches the node; secrets come from their
// variables or files.
func TestRunReadsTheFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	cfg := writeConfig(t, dir, `node: {tz: UTC}
log: {dir: off}
metrics: {addr: off}
telegram:
  token: env:MY_BOT
  api_url: http://127.0.0.1:9
  admin: telegram:user:123456
  reply: always
  rich: false
  reactions: {after: 2s, dwell: off}
agent:
  workspace: /var/lib/ganglion
  languages: /etc/ganglion/languages
  session_idle: 4h
  limits: {daily_budget_usd: 2.5, daily_turns: 100, daily_tokens: 500000}
  search: {mode: off, brave_key: env:MY_BRAVE}
sandbox: {deny: [/a, /b], hide_names: true}
`+models)
	for k, v := range map[string]string{
		"MY_BOT_FILE": write(t, "tg", "7001:from-file\n"), "OPENROUTER_API_KEY": " sk-or-secret\n", "MY_BRAVE": "BSA-key",
	} {
		t.Setenv(k, v)
	}
	b, _, _, err := captured(t, "run", "--roles", "core", "--config", cfg, "--db", "/data/g.db")
	if err != nil {
		t.Fatal(err)
	}
	a := b.Agent
	if b.DB != "/data/g.db" || b.Admin != "telegram:user:123456" || b.BotAPIURL != "http://127.0.0.1:9" || b.Languages != "/etc/ganglion/languages" ||
		b.TelegramReply != "always" || b.TelegramRich || b.MetricsAddr != "off" || b.BotToken != "7001:from-file" || b.LogDir != "" ||
		b.ReactionAfter != 2*time.Second || b.ReactionDwell != 0 || !b.SandboxHideNames || !slices.Equal(b.SandboxDeny, []string{"/a", "/b"}) {
		t.Fatalf("bootstrap %+v", b)
	}
	if a.Models == nil || len(a.Models.Models) != 2 || a.Keys["openrouter"] != "sk-or-secret" || a.Keys["claude"] != "" ||
		a.Workspace != "/var/lib/ganglion" || a.DailyBudgetUSD != 2.5 || a.DailyTurns != 100 || a.DailyTokens != 500000 ||
		a.SessionIdle != 4*time.Hour || a.TZ.String() != "UTC" || !a.SearchOff || a.BraveKey != "BSA-key" {
		t.Fatalf("agent %+v", a)
	}
	if b.Settings.Telegram.Admin != "telegram:user:123456" {
		t.Fatalf("the file does not reach the node: %+v", b.Settings)
	}
}

// --config and --db read their variables; a flag wins over its variable.
func TestFlagsOverrideTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_DB", "/from/env.db")
	t.Setenv("GANGLION_CONFIG", writeConfig(t, t.TempDir(), quiet+"agent: {limits: {daily_turns: 7}}\n"))
	b, _, _, err := captured(t, "run")
	if err != nil || b.DB != "/from/env.db" || b.Agent.DailyTurns != 7 {
		t.Fatalf("from the environment: %+v, %v", b, err)
	}
	b, _, _, err = captured(t, "run", "--db", "/from/flag.db", "--config", writeConfig(t, t.TempDir(), quiet+"agent: {session_idle: off}\n"))
	if err != nil || b.DB != "/from/flag.db" || b.Agent.DailyTurns != 300 || b.Agent.SessionIdle != 0 {
		t.Fatalf("from the flags: %+v, %v", b, err)
	}
}

// Without --config the file is ganglion.yaml beside the database.
func TestConfigBesideTheDatabase(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	writeConfig(t, dir, quiet+"node: {tz: UTC}\n")
	b, _, _, err := captured(t, "run", "--db", filepath.Join(dir, "g.db"))
	if err != nil || b.Agent.TZ.String() != "UTC" {
		t.Fatalf("tz %v, %v", b.Agent.TZ, err)
	}
}

func TestBadValuesNameTheirPath(t *testing.T) {
	for body, path := range map[string]string{
		"telegram: {admin: \"@igor\"}\n":         "telegram.admin",
		"telegram: {reply: sometimes}\n":         "telegram.reply",
		"node: {tz: Mars/Base}\n":                "node.tz",
		"agent: {limits: {daily_turns: many}}\n": "agent.limits.daily_turns",
		"log: {level: chatty}\n":                 "log.level",
		"agent: {search: {mode: google}}\n":      "agent.search.mode",
		"sandbox: {hide_names: yes-please}\n":    "sandbox.hide_names",
		"models: {providers: []}\n":              "models.models",
	} {
		t.Run(path, func(t *testing.T) {
			clearEnv(t)
			if _, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, body)...)...); err == nil ||
				!strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
				t.Fatalf("%q: %v, want an error naming %s", body, err, path)
			}
		})
	}
}

// A variable that held a setting before M4 stops the start: a stale
// deploy must not run on defaults (R-CFG-5).
func TestRetiredVariablesStopTheStart(t *testing.T) {
	clearEnv(t)
	t.Setenv("GANGLION_MODELS_FILE", "/srv/models.yaml")
	_, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet)...)...)
	if err == nil || err.Error() != "GANGLION_MODELS_FILE is no longer read: set models in ganglion.yaml" {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingProviderKeyFailsAtStart(t *testing.T) {
	clearEnv(t)
	_, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet+models)...)...)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestSecretErrorsNeverShowTheSecret(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "7001:very-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", write(t, "tg", "7001:very-secret"))
	_, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet)...)...)
	if err == nil || strings.Contains(err.Error(), "very-secret") || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("both token forms: %v", err)
	}
}

// A secret written into the file stops the start, and the error does not
// repeat it.
func TestALiteralSecretInTheFileStopsTheStart(t *testing.T) {
	clearEnv(t)
	_, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet+"telegram: {token: \"7001:very-secret\"}\n")...)...)
	if err == nil || strings.Contains(err.Error(), "very-secret") || !strings.Contains(err.Error(), "telegram.token") {
		t.Fatalf("a literal token: %v", err)
	}
}

func TestRunRejectsUnknownRole(t *testing.T) {
	clearEnv(t)
	if _, _, _, err := captured(t, append([]string{"run", "--roles", "core,brain"}, nodeFiles(t, quiet)...)...); err == nil || !strings.Contains(err.Error(), "brain") {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestHelpListsTheSettingsAndTheirVariables(t *testing.T) {
	_, _, out, err := captured(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--config", "GANGLION_CONFIG", "--db", "GANGLION_DB", "--roles", "ganglion.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("run --help lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"--tz", "--models-file", "--log-level", "--daily-turns"} {
		if strings.Contains(out, gone) {
			t.Errorf("run --help still has %s: settings live in ganglion.yaml", gone)
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
	b, _, _, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet+"agent: {limits: {daily_turns: 050, daily_tokens: 0100}}\n")...)...)
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
	_, _, out, err := captured(t, append([]string{"run"}, nodeFiles(t, quiet+"telegram: {token: env:MY_BOT}\n")...)...)
	if err != nil || !strings.Contains(out, "no models section in ganglion.yaml; the agent is off") ||
		!strings.Contains(out, "no MY_BOT; the Telegram transport is off") {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// R-OBS-6: by default the logs go to <the database's directory>/logs as
// JSON lines, beside stdout.
func TestRunWritesLogsToDisk(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	b, _, out, err := captured(t, "run", "--db", filepath.Join(dir, "ganglion.db"), "--config", writeConfig(t, dir, "metrics: {addr: off}\n"))
	if err != nil || !strings.Contains(out, "the bot echoes") {
		t.Fatalf("%v:\n%s", err, out)
	}
	if b.Metrics == nil {
		t.Fatal("no metrics renderer reached the node with metrics.addr off")
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
	if _, _, _, err := captured(t, "run", "--db", filepath.Join(dir, "ganglion.db"), "--config", writeConfig(t, dir, quiet)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("logs/ exists with log.dir off: %v", err)
	}
}

// log.level sets the least severe level written.
func TestLogLevelFromTheFile(t *testing.T) {
	clearEnv(t)
	_, _, out, err := captured(t, append([]string{"run"}, nodeFiles(t, "metrics: {addr: off}\nlog: {level: error, dir: off}\n")...)...)
	if err != nil || strings.Contains(out, "level=WARN") || strings.Contains(out, "level=INFO") {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// TestTheSandboxHelperIsHidden: nobody types `ganglion __sandbox`; the node
// starts it for each command (ADR-0039).
func TestTheSandboxHelperIsHidden(t *testing.T) {
	_, _, out, err := captured(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, sandbox.HelperArg) {
		t.Fatalf("the help lists the helper:\n%s", out)
	}
}

// A relative database path gives an absolute claude folder: the CLI runs
// inside it and the sandbox, the janitor and host.stats must see the same
// folder.
func TestBootstrapClaudeDirIsAbsolute(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(dir, "bin"), quiet)
	b, _, _, err := captured(t, "run", "--roles", "core", "--db", "bin/live.db")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("bin/claude")
	if b.ClaudeDir != want {
		t.Fatalf("ClaudeDir %q, want %q", b.ClaudeDir, want)
	}
}
