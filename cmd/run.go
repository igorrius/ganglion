package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/router/rules"
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
			"TELEGRAM_BOT_TOKEN and each provider's key_env are read from the variable or from <NAME>_FILE.",
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
			logger := slog.New(telemetry.LogHandler(slog.NewTextHandler(out, &slog.HandlerOptions{Level: root.Level()}))).
				With("version", in.Version, "commit", shortCommit(in))
			stop := startTelemetry(ctx, b.MetricsAddr, in, logger)
			defer stop()
			announce(logger, b)
			return start(ctx, roles, b, logger)
		},
	}
}

// bootstrap turns the flags into the node's bootstrap settings, reading
// the secrets and the model catalogue.
func bootstrap(v flags.Run, src secrets.Source, readFile func(string) ([]byte, error)) (config.Bootstrap, error) {
	b := config.Bootstrap{DB: v.DB, BotAPIURL: v.TelegramAPIURL, Admin: v.Admin, MetricsAddr: v.MetricsAddr, TelegramReply: v.TelegramReply}
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
		DailyTokens: v.DailyTokens, SessionIdle: idle, TZ: tz}
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
func startTelemetry(ctx context.Context, addr string, in buildinfo.Info, logger *slog.Logger) (stop func()) {
	cfg := telemetry.Config{Addr: addr, Service: "ganglion", Version: in.Version, Commit: in.Commit}
	tel, err := telemetry.Setup(ctx, cfg)
	if err != nil {
		logger.Warn("telemetry: metrics are off", "addr", addr, "err", err)
		cfg.Addr = "off"
		if tel, err = telemetry.Setup(ctx, cfg); err != nil {
			return func() {}
		}
	} else if a := tel.Addr(); a != "" {
		logger.Info("telemetry: serving", "metrics", "http://"+a+"/metrics", "dashboard", "http://"+a+"/webmetrics/")
	}
	return func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(sctx)
	}
}

func shortCommit(in buildinfo.Info) string { return in.Commit[:min(7, len(in.Commit))] }
