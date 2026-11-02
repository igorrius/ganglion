package node

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/outbox"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/turns"
)

// conflictAlert is sent to every admin when Telegram polling stops on 409.
const conflictAlert = "Polling stopped: another process or a webhook uses this bot token. Stop it and restart ganglion."

// pruneEvery is how often finished queue rows are pruned.
const pruneEvery = 24 * time.Hour

// Run starts the given roles and blocks until ctx is cancelled, then
// returns nil. An error from a component ends Run with that error.
//
// The core role runs the echo bot of M2: the inbox and outbox, the turn
// loop with core.Echo, the outbox sender and, with a bot token, the
// Telegram transport behind the access list. Edge and worker have no
// behaviour yet (docs/spec.md, section 10).
func Run(ctx context.Context, roles []Role, cfg Config, logger *slog.Logger) error {
	if len(roles) == 0 {
		return errors.New("node: no roles to run")
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}
	logger.Info("node started", "roles", strings.Join(names, ","))
	defer logger.Info("node stopped")

	if !slices.Contains(roles, Core) {
		<-ctx.Done()
		return nil
	}
	if err := runCore(ctx, cfg, logger); err != nil && ctx.Err() == nil {
		return err
	}
	return nil // a stop was asked for; errors on the way out do not matter
}

type component struct {
	name string
	run  func(context.Context) error
}

func runCore(ctx context.Context, cfg Config, logger *slog.Logger) error {
	started := time.Now()
	unlock, err := lockDB(cfg.DB)
	if err != nil {
		return err
	}
	defer unlock()
	db, err := sqlitedb.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		return err
	}
	// This process owns the database: leases in it belong to a process
	// that died (ADR-0019).
	if n, err := q.ReleaseLeases(ctx); err != nil {
		return err
	} else if n > 0 {
		logger.Warn("node: released leases left by a previous process", "events", n)
	}
	acl, err := access.New(ctx, db)
	if err != nil {
		return err
	}
	if cfg.Admin != "" {
		if err := acl.Allow(ctx, cfg.Admin, access.RoleAdmin, ""); err != nil {
			return err
		}
	}

	sender := outbox.NewSender(q, q.OutboxReady(), logger)
	handler, loopOpts, err := buildHandler(ctx, cfg.Agent, db, q, acl, logger)
	if err != nil {
		return err
	}
	loop := core.NewLoop(q, q.InboxReady(), handler, logger, loopOpts...)
	components := []component{
		{"sender", sender.Run},
		{"loop", loop.Run},
		{"prune", func(ctx context.Context) error { return prune(ctx, q, logger) }},
	}

	if cfg.BotToken == "" {
		logger.Warn("node: no TELEGRAM_BOT_TOKEN; the Telegram transport is off")
	} else {
		tr, err := telegram.New(cfg.BotToken, telegram.WithBaseURL(cfg.BotAPIURL), telegram.WithLogger(logger))
		if err != nil {
			return err
		}
		sender.Register(tr)
		sink := acl.Filter(q.Put, logger)
		components = append(components, component{"telegram", func(ctx context.Context) error {
			err := tr.Run(ctx, sink)
			if !errors.Is(err, telegram.ErrConflict) {
				return err
			}
			// Do not exit: a supervisor would restart the process into the
			// same fight. Tell the admins and keep sending.
			logger.Error("node: Telegram polling stopped; another process or a webhook holds the token", "err", err)
			return alertAdmins(ctx, acl, q, started)
		}})
	}
	return runAll(ctx, components)
}

// runAll runs the components until ctx ends or one of them fails; then it
// stops the rest and returns the first error. A component that returns nil
// before ctx ends just stops.
func runAll(ctx context.Context, components []component) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, c := range components {
		wg.Go(func() {
			err := c.run(ctx)
			if err != nil && ctx.Err() == nil {
				once.Do(func() { firstErr = fmt.Errorf("%s: %w", c.name, err) })
				cancel()
			}
		})
	}
	wg.Wait()
	return firstErr
}

// telegramPrivate returns the private chat of a Telegram principal: a
// user's private chat has the user's id.
func telegramPrivate(principal string) (transport, conv string, ok bool) {
	idStr, ok := strings.CutPrefix(principal, "telegram:user:")
	if !ok {
		return "", "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", "", false
	}
	return "telegram", telegram.ConvKey(id, 0), true
}

// buildHandler returns the agent when a model catalogue is configured, and
// the M2 echo otherwise.
func buildHandler(ctx context.Context, ac AgentConfig, db *sql.DB, q *sqlqueue.Queue, acl *access.List, logger *slog.Logger) (core.Handler, []core.Option, error) {
	if ac.Catalog == nil {
		logger.Warn("node: no GANGLION_MODELS_FILE; the agent is off and the bot echoes")
		return core.EchoHandler, nil, nil
	}
	turnsLog, err := turns.New(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	archive, err := agent.NewArchive(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	providers := map[string]provider.Provider{}
	for _, p := range ac.Catalog.Providers {
		switch p.Type {
		case rules.TypeClaudeCLI:
			var opts []claudecli.Option
			if p.Command != "" {
				opts = append(opts, claudecli.WithCommand(p.Command))
			}
			providers[p.Name] = claudecli.New(p.Name, opts...)
		default:
			providers[p.Name] = openai.New(p.Name, p.BaseURL, ac.Keys[p.Name])
		}
	}
	tz := ac.TZ
	if tz == nil {
		tz = time.UTC
	}
	rt, err := rules.New(ac.Catalog.RouterModels(), rules.WithBudget(turnsLog, ac.DailyBudgetUSD, tz))
	if err != nil {
		return nil, nil, err
	}
	ag := agent.New(agent.Config{
		SystemPrompt: ac.SystemPrompt, DailyTurns: ac.DailyTurns, DailyTokens: ac.DailyTokens,
		SessionIdle: ac.SessionIdle, Location: tz,
	}, agent.Deps{
		States: q, Router: rt, Providers: providers, Turns: turnsLog, Archive: archive,
		Admins: acl.Admins, AlertConv: telegramPrivate, Log: logger,
	})
	giveUp := func(e event.Event) []event.Outgoing {
		out := []event.Outgoing{{IdemKey: e.IdemKey + ":reply:0", Transport: e.Source, Conv: e.Conv,
			Text: "Sorry, something went wrong with that message."}}
		admins, _ := acl.Admins(context.Background())
		for _, adm := range admins {
			if transport, conv, ok := telegramPrivate(adm); ok {
				out = append(out, event.Outgoing{IdemKey: "alert:give-up:" + e.ID + ":" + adm, Transport: transport, Conv: conv,
					Text: "Gave up on event " + e.ID + " in " + e.Conv + " after " + strconv.Itoa(core.DefaultMaxAttempts) + " attempts."})
			}
		}
		return out
	}
	logger.Info("node: agent on", "providers", len(providers), "models", len(ac.Catalog.Models))
	return ag.Handle, []core.Option{core.WithMaxAttempts(core.DefaultMaxAttempts, giveUp)}, nil
}

// alertAdmins enqueues the conflict alert to the private chat of every
// Telegram admin, once per process start.
func alertAdmins(ctx context.Context, acl *access.List, q *sqlqueue.Queue, started time.Time) error {
	admins, err := acl.Admins(ctx)
	if err != nil {
		return err
	}
	var msgs []event.Outgoing
	for _, p := range admins {
		transport, conv, ok := telegramPrivate(p)
		if !ok {
			continue
		}
		msgs = append(msgs, event.Outgoing{
			IdemKey:   "alert:telegram-conflict:" + strconv.FormatInt(started.UnixMilli(), 10) + ":" + p,
			Transport: transport, Conv: conv, Text: conflictAlert,
		})
	}
	if len(msgs) == 0 {
		return nil
	}
	return q.Enqueue(ctx, msgs...)
}

// prune deletes finished queue rows at start and then once a day.
func prune(ctx context.Context, q *sqlqueue.Queue, logger *slog.Logger) error {
	for {
		if n, err := q.Prune(ctx, sqlqueue.Retention); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Error("node: prune failed", "err", err)
		} else if n > 0 {
			logger.Info("node: pruned finished queue rows", "rows", n)
		}
		timer := time.NewTimer(pruneEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
