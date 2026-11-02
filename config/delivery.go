package config

import (
	"context"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/outbox"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// pruneEvery is how often finished queue rows are pruned.
const pruneEvery = 24 * time.Hour

// Admins provides alert addressing: every admin of the access list, in
// their Telegram private chat.
func Admins() do.Provider[alert.Admins] {
	return func(i do.Injector) (alert.Admins, error) {
		acl, err := do.Invoke[*access.List](i)
		if err != nil {
			return alert.Admins{}, err
		}
		return alert.Admins{List: acl.Admins, Private: telegram.PrivateConv}, nil
	}
}

// Telegram provides the Telegram transport. It is invoked only when a bot
// token is set.
func Telegram() do.Provider[*telegram.Transport] {
	return func(i do.Injector) (*telegram.Transport, error) {
		var err error
		b, logger := need[Bootstrap](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		return telegram.New(b.BotToken, telegram.WithBaseURL(b.BotAPIURL), telegram.WithLogger(logger),
			telegram.WithReplyMode(telegram.ReplyMode(b.TelegramReply)))
	}
}

// Sender provides the outbox sender with the transports that are on. A
// message for a transport that is off waits in the outbox.
func Sender() do.Provider[*outbox.Sender] {
	return func(i do.Injector) (*outbox.Sender, error) {
		var err error
		b, q, logger := need[Bootstrap](i, &err), need[*sqlqueue.Queue](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		s := outbox.NewSender(q, q.OutboxReady(), logger)
		if b.BotToken != "" {
			tr, err := do.Invoke[*telegram.Transport](i)
			if err != nil {
				return nil, err
			}
			s.Register(tr)
		}
		return s, nil
	}
}

// TurnHook provides what runs around a turn: "typing…" in Telegram for a
// Telegram message when the transport is on, nothing otherwise.
func TurnHook() do.Provider[core.TurnHook] {
	return func(i do.Injector) (core.TurnHook, error) {
		b, err := do.Invoke[Bootstrap](i)
		if err != nil || b.BotToken == "" {
			return core.NoTurnHook, err
		}
		tr, err := do.Invoke[*telegram.Transport](i)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, e event.Event) func() {
			if e.Source != telegram.Name || e.Kind != event.KindMessage {
				return func() {}
			}
			tctx, stop := context.WithCancel(ctx)
			go tr.Typing(tctx, e.Conv)
			return stop
		}, nil
	}
}

// Loop provides the turn loop over the handler.
func Loop() do.Provider[*core.Loop] {
	return func(i do.Injector) (*core.Loop, error) {
		var err error
		q, h, hook := need[*sqlqueue.Queue](i, &err), need[core.Handler](i, &err), need[core.TurnHook](i, &err)
		giveUp, logger := need[GiveUp](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		return core.NewLoop(q, q.InboxReady(), h, logger, core.WithTurnHook(hook),
			core.WithMaxAttempts(core.DefaultMaxAttempts, giveUp)), nil
	}
}

// CoreComponents provides the core role's components: the sender, the
// turn loop, the pruner and, with a bot token, Telegram polling behind
// the access list.
func CoreComponents() do.Provider[[]node.Component] {
	return func(i do.Injector) ([]node.Component, error) {
		var err error
		b, q, logger := need[Bootstrap](i, &err), need[*sqlqueue.Queue](i, &err), need[*slog.Logger](i, &err)
		sender, loop := need[*outbox.Sender](i, &err), need[*core.Loop](i, &err)
		if err != nil {
			return nil, err
		}
		components := []node.Component{
			node.Named("sender", sender.Run),
			node.Named("loop", loop.Run),
			sqlqueue.Pruner{Queue: q, Every: pruneEvery, Log: logger},
		}
		if b.BotToken == "" {
			logger.Warn("node: no TELEGRAM_BOT_TOKEN; the Telegram transport is off")
			return components, nil
		}
		tr, acl, admins, started := need[*telegram.Transport](i, &err), need[*access.List](i, &err),
			need[alert.Admins](i, &err), need[ProcessStart](i, &err)
		if err != nil {
			return nil, err
		}
		return append(components, node.Poller{
			Transport: tr, Sink: acl.Filter(q.Put, logger), Admins: admins,
			Enqueue: q.Enqueue, Started: time.Time(started), Log: logger,
		}), nil
	}
}
