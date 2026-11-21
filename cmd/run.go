package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/internal/memlimit"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/rustenv"
	"github.com/igorrius/ganglion/logfile"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry"
	"github.com/urfave/cli/v3"
)

func runCommand(out, errOut io.Writer, start startFunc) *cli.Command {
	var v flags.Run
	return &cli.Command{
		Name:  "run",
		Usage: "run a node with the given roles until a signal",
		Description: "Settings come from ganglion.yaml (--config; default: beside the database). Secrets never are in it:\n" +
			"the file names each one as env:NAME, and the node reads NAME or the file named by NAME_FILE,\n" +
			"or as store:NAME, one of the node's own secrets in its secret store (ganglion secret set NAME).",
		Flags: v.Flags(),
		Action: func(ctx context.Context, _ *cli.Command) error {
			launch(ctx, v.DB, errOut) // first: the process may be replaced by the active release
			roles, err := node.ParseRoles(v.Roles)
			if err != nil {
				return err
			}
			b, err := bootstrap(ctx, v, secrets.OSEnv(), os.Getenv, os.ReadFile)
			if err != nil {
				return err
			}
			b.MemoryLimit = memlimit.Apply(b.MemoryLimit)
			in := buildinfo.Read()
			logger, closeLogs, err := newLogger(out, logLevel(b.Settings.Log.Level), b)
			if err != nil {
				return err
			}
			defer closeLogs()
			logger = logger.With("version", in.Version, "commit", shortCommit(in))
			if err := sandbox.Harden(); err != nil {
				logger.Warn("node: a script can read this process's environment from /proc", "err", err)
			}
			tel, stop := startTelemetry(ctx, b.MetricsAddr, in, logger)
			defer stop()
			if tel != nil {
				b.Metrics = tel.Renderer()
			}
			announce(logger, b)
			err = start(ctx, roles, b, logger)
			if errors.Is(err, node.ErrRestart) {
				cleanStop(v.DB, in, errOut)
			}
			return err
		},
	}
}

// logLevel is log.level as a slog level; settings checked the value.
func logLevel(v string) slog.Level {
	var l slog.Level
	_ = l.UnmarshalText([]byte(v))
	return l
}

// newLogger logs as text to out and, unless log.dir is off, as JSON lines
// to the rotated files on disk (R-OBS-6, ADR-0043). Both sides carry trace
// ids and hide the secrets the node read and the patterns of
// redact.String. The disk comes first, so a line on stdout is already in
// the file when a kill -9 lands.
func newLogger(out io.Writer, level slog.Level, b config.Bootstrap) (*slog.Logger, func(), error) {
	values := redact.Values(b.SecretValues()...)
	hide := func(s string) string { return values(redact.String(s)) }
	opts := &slog.HandlerOptions{Level: level}
	wrap := func(h slog.Handler) slog.Handler { return telemetry.LogHandler(telemetry.RedactHandler(h, hide)) }
	stdout := wrap(slog.NewTextHandler(out, opts))
	if b.LogDir == "" {
		return slog.New(stdout), func() {}, nil
	}
	rot, err := logfile.Open(logfile.DefaultConfig(b.LogDir), time.Now)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: log.dir: %w", settings.FileName, err)
	}
	return slog.New(slog.NewMultiHandler(wrap(slog.NewJSONHandler(rot, opts)), stdout)), func() { _ = rot.Close() }, nil
}

// bootstrap reads the config file and the secrets it references into
// config.Bootstrap. A retired variable fails first: a stale deploy must
// not run on defaults (R-CFG-5).
func bootstrap(ctx context.Context, v flags.Run, src secrets.Source, getenv func(string) string, read func(string) ([]byte, error)) (config.Bootstrap, error) {
	if err := settings.Retired(getenv); err != nil {
		return config.Bootstrap{}, err
	}
	f, err := loadSettings(v.Config, v.DB, read)
	if err != nil {
		return config.Bootstrap{}, err
	}
	limit := memlimit.Decide(getenv("GOMEMLIMIT"), uint64(f.Node.MemoryLimit), read)
	store, done, err := storeSource(ctx, f, v.DB, src)
	if err != nil {
		return config.Bootstrap{}, err
	}
	defer done()
	b, err := fromFile(f, v.DB, limit, settings.Sources{Env: src, Store: store})
	if err != nil {
		return config.Bootstrap{}, err
	}
	b.Build = rustenv.LocalFromEnv(getenv)
	b.ImageEnv = imageEnvOr0(v.ImageEnv, read)
	b.SettingsPath, b.Secrets, b.MasterKey = settingsPath(v.Config, v.DB), src, masterKey(v.DB)
	return b, nil
}

// memoryDB is the database path of a node that keeps nothing on disk.
const memoryDB = ":memory:"

// masterKey is where the secret store's key file is for the database at
// db: beside it, or nowhere for a database in memory.
func masterKey(db string) string {
	if db == memoryDB {
		return ""
	}
	return vault.KeyPath(db)
}

// storeSource is where the file's store: references are read: the node
// scope of the secret store in the database at db (ADR-0077). The node's
// components need these values before the container exists, so the
// database is opened here for a moment; a file without a store: reference
// opens nothing. The caller calls done when it has read them. A store
// whose key does not fit reads every reference as not set; the node says
// so at start and in /status.
func storeSource(ctx context.Context, f settings.File, db string, src secrets.Source) (store secrets.Source, done func(), err error) {
	if len(f.StoreNames()) == 0 || db == memoryDB {
		return secrets.None{}, func() {}, nil
	}
	key, _, err := vault.LoadKey(src, masterKey(db))
	if err != nil {
		return nil, nil, err
	}
	conn, err := sqlitedb.Open(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	v, err := vault.Open(ctx, conn, key, time.Now)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return v.Source(secrets.ScopeNode), func() { _ = conn.Close() }, nil
}

// loadSettings reads the file the operator named, which must exist, or
// else the one beside the database at db, which may be missing (R-CFG-1).
func loadSettings(named, db string, read settings.ReadFunc) (settings.File, error) {
	if named != "" {
		return settings.Load(named, read)
	}
	return settings.LoadOptional(settingsPath(named, db), read)
}

// settingsPath is the file the node reads its settings from: the one the
// operator named, else the one beside the database at db.
func settingsPath(named, db string) string {
	if named != "" {
		return named
	}
	return filepath.Join(filepath.Dir(db), settings.FileName)
}

// fromFile maps the file to the node's bootstrap settings for the
// database at db, with the decided memory limit, reading the secrets
// through src.
func fromFile(f settings.File, db string, limit memlimit.Limit, src settings.Sources) (config.Bootstrap, error) {
	b := config.Bootstrap{DB: db, Languages: f.Agent.Languages, BotAPIURL: f.Telegram.APIURL, Admin: f.Telegram.Admin,
		HTTPAddr: f.HTTP.Addr, PublicURL: f.HTTP.PublicURL, TelegramWebhook: f.Telegram.Updates == settings.UpdatesWebhook,
		MetricsAddr: f.Metrics.Addr, TelegramReply: f.Telegram.Reply, TelegramRich: f.Telegram.Rich,
		ReactionAfter: time.Duration(f.Telegram.Reactions.After), ReactionDwell: time.Duration(f.Telegram.Reactions.Dwell),
		SandboxDeny: f.Sandbox.Deny, SandboxHideNames: f.Sandbox.HideNames, UpdateCheck: f.Update.Check,
		ModulesDir:  filepath.Join(filepath.Dir(db), "modules"),
		MemoryLimit: limit, Settings: f,
		Timeouts: config.Timeouts{Call: time.Duration(f.Agent.CallTimeout), Turn: time.Duration(f.Agent.TurnTimeout), Steps: f.Agent.MaxSteps,
			Handler: f.Agent.HandlerTimeout()}}
	// Absolute: the CLI runs inside it, and the sandbox, the janitor and
	// host.stats must all name the same folder.
	var err error
	if b.ClaudeDir, err = filepath.Abs(filepath.Join(filepath.Dir(db), "claude")); err != nil {
		return config.Bootstrap{}, err
	}
	if dir, on := f.LogDir(db); on {
		b.LogDir = dir
	}
	if b.BotToken, err = f.Telegram.Token.Resolve(src); err != nil {
		return config.Bootstrap{}, err
	}
	if f.Backup != nil {
		if b.BackupSecret, err = f.Backup.Secret(src); err != nil {
			return config.Bootstrap{}, err
		}
	}
	if b.BusCredentials, err = f.Bus.CredentialsText(src); err != nil {
		return config.Bootstrap{}, err
	}
	if db != memoryDB {
		b.BusDir = bus.StorePath(filepath.Dir(db))
	}
	b.Agent, err = agentSettings(f, src)
	return b, err
}

// agentSettings maps the file's agent and models sections, reading the
// search key and the providers' keys through src.
func agentSettings(f settings.File, src settings.Sources) (config.AgentSettings, error) {
	tz, err := time.LoadLocation(f.Node.TZ) // checked by settings
	if err != nil {
		return config.AgentSettings{}, fmt.Errorf("%s: node.tz: %w", settings.FileName, err)
	}
	l := f.Agent.Limits
	a := config.AgentSettings{Workspace: f.Agent.Workspace, DailyBudgetUSD: float64(l.DailyBudgetUSD), DailyTurns: int(l.DailyTurns),
		DailyTokens: int(l.DailyTokens), SessionIdle: time.Duration(f.Agent.SessionIdle), TZ: tz, SearchOff: f.Agent.Search.Mode == "off",
		Models: f.Models, Memory: config.MemorySettings{PromptBudget: int(f.Memory.PromptBudget), Review: f.Memory.Review.Enabled,
			Quiet: time.Duration(f.Memory.Review.Quiet), ArchiveDays: int(f.Memory.ArchiveDays)}}
	if a.BraveKey, err = f.Agent.Search.BraveKey.Resolve(src); err != nil {
		return config.AgentSettings{}, err
	}
	if f.Models != nil {
		a.Keys, err = f.Models.Keys(src)
	}
	return a, err
}

// announce says which optional parts the settings turn on or leave off.
func announce(logger *slog.Logger, b config.Bootstrap) {
	if m := b.Agent.Models; m != nil {
		logger.Info("node: agent on", "providers", len(m.Providers), "models", len(m.Models))
	} else {
		logger.Warn("node: no models section in " + settings.FileName + "; the agent is off and the bot echoes")
	}
	if b.BotToken == "" {
		logger.Warn("node: no " + string(b.Settings.Telegram.Token) + "; the Telegram transport is off")
	}
	if b.Settings.HTTP.On() {
		logger.Info("node: public listener on", "addr", b.HTTPAddr, "telegram_webhook", b.TelegramWebhook)
	}
	l := b.MemoryLimit
	logger.Info("node: memory limit", "bytes", l.Bytes, "source", string(l.Source))
}

// startTelemetry serves metrics and returns the stop. Watching the node
// must never stop it: a busy port runs the node without metrics.
func startTelemetry(ctx context.Context, addr string, in buildinfo.Info, logger *slog.Logger) (_ *telemetry.Telemetry, stop func()) {
	cfg := telemetry.Config{Addr: addr, Service: "ganglion", Version: in.Version, Commit: in.Commit, Logger: logger}
	tel, err := telemetry.Setup(ctx, cfg)
	if err != nil {
		logger.Warn("telemetry: metrics are off", "addr", addr, "err", err)
		cfg.Addr = "off"
		if tel, err = telemetry.Setup(ctx, cfg); err != nil {
			return nil, func() {}
		}
	} else if a := tel.Addr(); a != "" {
		logger.Info("telemetry: serving", "metrics", "http://"+a+"/metrics", "dashboard", "http://"+a+"/webmetrics/")
	}
	return tel, func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(sctx)
	}
}

func shortCommit(in buildinfo.Info) string { return in.Commit[:min(7, len(in.Commit))] }
