package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/logfile"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/telemetry"
	"github.com/urfave/cli/v3"
)

func runCommand(root *flags.Root, out io.Writer, start startFunc) *cli.Command {
	var v flags.Run
	def := agent.DefaultConfig()
	return &cli.Command{
		Name:  "run",
		Usage: "run a node with the given roles until a signal",
		Description: "Settings come from flags or their environment variables. Secrets never are flags:\n" +
			"TELEGRAM_BOT_TOKEN, BRAVE_API_KEY and each provider's key_env are read from the variable or from <NAME>_FILE.",
		Flags: v.Flags(def.DailyTurns, def.DailyTokens, def.SessionIdle),
		Action: func(ctx context.Context, _ *cli.Command) error {
			roles, err := node.ParseRoles(v.Roles)
			if err != nil {
				return err
			}
			b, err := bootstrap(v, secrets.OSEnv(), os.ReadFile)
			if err != nil {
				return err
			}
			in := buildinfo.Read()
			logger, closeLogs, err := newLogger(out, root.Level(), b)
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
			return start(ctx, roles, b, logger)
		},
	}
}

// newLogger logs as text to out and, unless --log-dir is off, as JSON
// lines to the rotated files on disk (R-OBS-6, ADR-0043). Both sides
// carry trace ids and hide the secrets the node read and the patterns of
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
		return nil, nil, fmt.Errorf("GANGLION_LOG_DIR: %w", err)
	}
	return slog.New(slog.NewMultiHandler(wrap(slog.NewJSONHandler(rot, opts)), stdout)), func() { _ = rot.Close() }, nil
}

// bootstrap turns the flags into the node's bootstrap settings, reading
// the secrets and the model catalogue.
func bootstrap(v flags.Run, src secrets.Source, readFile func(string) ([]byte, error)) (config.Bootstrap, error) {
	b := config.Bootstrap{DB: v.DB, Languages: v.Languages, BotAPIURL: v.TelegramAPIURL, Admin: v.Admin, MetricsAddr: v.MetricsAddr, TelegramReply: v.TelegramReply, TelegramRich: v.TelegramRich == "on",
		ReactionAfter: v.ReactionAfter, ReactionDwell: v.ReactionDwell}
	b.ModulesDir = filepath.Join(filepath.Dir(v.DB), "modules")
	b.ClaudeDir = filepath.Join(filepath.Dir(v.DB), "claude")
	b.SandboxDeny = filepath.SplitList(v.SandboxDeny)
	b.SandboxHideNames = v.SandboxHideNames == "on"
	if dir, on := flags.LogDir(v.DB, v.LogDir); on {
		b.LogDir = dir
	}
	var err error
	if b.BotToken, err = src.Secret("TELEGRAM_BOT_TOKEN"); err != nil {
		return config.Bootstrap{}, err
	}
	tz, err := time.LoadLocation(v.TZ) // checked by the flag
	if err != nil {
		return config.Bootstrap{}, fmt.Errorf("GANGLION_TZ: %w", err)
	}
	idle, err := flags.Idle(v.SessionIdle)
	if err != nil {
		return config.Bootstrap{}, fmt.Errorf("GANGLION_SESSION_IDLE: %w", err)
	}
	b.Agent = config.AgentSettings{Workspace: v.Workspace, DailyBudgetUSD: v.DailyBudgetUSD, DailyTurns: v.DailyTurns,
		DailyTokens: v.DailyTokens, SessionIdle: idle, TZ: tz, SearchOff: v.Search == "off"}
	if b.Agent.BraveKey, err = src.Secret("BRAVE_API_KEY"); err != nil {
		return config.Bootstrap{}, err
	}
	if v.ModelsFile == "" {
		return b, nil
	}
	b.Agent.Catalog, b.Agent.Keys, err = catalogue(v.ModelsFile, src, readFile)
	return b, err
}

// catalogue loads the model catalogue and its providers' keys. A provider
// whose key_env is not set fails the start.
func catalogue(path string, src secrets.Source, readFile func(string) ([]byte, error)) (*rules.Catalog, map[string]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("GANGLION_MODELS_FILE: %w", err)
	}
	cat, err := rules.LoadCatalog(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("GANGLION_MODELS_FILE %s: %w", path, err)
	}
	keys := map[string]string{}
	for _, p := range cat.Providers {
		if p.KeyEnv == "" {
			continue
		}
		key, err := src.Secret(p.KeyEnv)
		if err != nil {
			return nil, nil, err
		}
		if key == "" {
			return nil, nil, fmt.Errorf("%s (key_env of provider %s) is not set; set it or %s_FILE", p.KeyEnv, p.Name, p.KeyEnv)
		}
		keys[p.Name] = key
	}
	return &cat, keys, nil
}

// announce says which optional parts the settings turn on or leave off.
func announce(logger *slog.Logger, b config.Bootstrap) {
	if cat := b.Agent.Catalog; cat != nil {
		logger.Info("node: agent on", "providers", len(cat.Providers), "models", len(cat.Models))
	} else {
		logger.Warn("node: no GANGLION_MODELS_FILE; the agent is off and the bot echoes")
	}
	if b.BotToken == "" {
		logger.Warn("node: no TELEGRAM_BOT_TOKEN; the Telegram transport is off")
	}
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
