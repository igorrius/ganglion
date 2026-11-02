package config

import (
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/outbox"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// pruneEvery is how often finished queue rows are pruned.
const pruneEvery = 24 * time.Hour

// Admins provides alert addressing: the access list's admins in their
// Telegram private chats.
func Admins() do.Provider[alert.Admins] {
	return func(i do.Injector) (alert.Admins, error) {
		acl, err := do.Invoke[*access.List](i)
		if err != nil {
			return alert.Admins{}, err
		}
		return alert.Admins{List: acl.Admins, Private: telegram.PrivateConv}, nil
	}
}

// Telegram provides the Telegram transport.
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

// Transports provides the transports that are on.
func Transports() do.Provider[[]transport.Transport] {
	return choose[[]transport.Transport](telegramOrNone)
}

// TelegramTransports provides Telegram as the only transport.
func TelegramTransports() do.Provider[[]transport.Transport] {
	return func(i do.Injector) ([]transport.Transport, error) {
		tr, err := do.Invoke[*telegram.Transport](i)
		return []transport.Transport{tr}, err
	}
}

// NoTransports provides no transports.
func NoTransports() do.Provider[[]transport.Transport] { return value[[]transport.Transport](nil) }

// Sender provides the outbox sender with the transports that are on.
func Sender() do.Provider[*outbox.Sender] {
	return func(i do.Injector) (*outbox.Sender, error) {
		var err error
		q, ts, logger := need[*sqlqueue.Queue](i, &err), need[[]transport.Transport](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		s := outbox.NewSender(q, q.OutboxReady(), logger)
		for _, t := range ts {
			s.Register(t)
		}
		return s, nil
	}
}

// TurnHook provides what runs around a turn.
func TurnHook() do.Provider[core.TurnHook] { return choose[core.TurnHook](telegramOrNone) }

// TypingHook provides Telegram's "typing…" as the turn hook.
func TypingHook() do.Provider[core.TurnHook] {
	return func(i do.Injector) (core.TurnHook, error) {
		tr, err := do.Invoke[*telegram.Transport](i)
		if err != nil {
			return nil, err
		}
		return tr.TypingHook, nil
	}
}

// NoTurnHook provides a turn hook that does nothing.
func NoTurnHook() do.Provider[core.TurnHook] { return value[core.TurnHook](core.NoTurnHook) }

// Loop provides the turn loop over the handler.
func Loop() do.Provider[*core.Loop] {
	return func(i do.Injector) (*core.Loop, error) {
		var err error
		q, h, hook := need[*sqlqueue.Queue](i, &err), need[core.Handler](i, &err), need[core.TurnHook](i, &err)
		giveUp, logger := need[core.GiveUp](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		return core.NewLoop(q, q.InboxReady(), h, logger, core.WithTurnHook(hook),
			core.WithMaxAttempts(core.DefaultMaxAttempts, giveUp)), nil
	}
}

// Inbound are the components that bring messages into the inbox.
type Inbound []node.Component

// Receivers provides the inbound components that are on.
func Receivers() do.Provider[Inbound] { return choose[Inbound](telegramOrNone) }

// TelegramReceivers provides Telegram polling behind the access list.
func TelegramReceivers() do.Provider[Inbound] {
	return func(i do.Injector) (Inbound, error) {
		var err error
		tr, q, acl := need[*telegram.Transport](i, &err), need[*sqlqueue.Queue](i, &err), need[*access.List](i, &err)
		admins, started, logger := need[alert.Admins](i, &err), need[ProcessStart](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		return Inbound{node.Poller{
			Transport: tr, Sink: acl.Filter(q.Put, logger), Admins: admins,
			Enqueue: q.Enqueue, Started: time.Time(started), Log: logger,
		}}, nil
	}
}

// NoReceivers provides no inbound components.
func NoReceivers() do.Provider[Inbound] { return value[Inbound](nil) }

// CoreComponents provides the core role's components: the sender, the
// turn loop, the pruner and the receivers.
func CoreComponents() do.Provider[[]node.Component] {
	return func(i do.Injector) ([]node.Component, error) {
		var err error
		q, logger := need[*sqlqueue.Queue](i, &err), need[*slog.Logger](i, &err)
		sender, loop, receivers := need[*outbox.Sender](i, &err), need[*core.Loop](i, &err), need[Inbound](i, &err)
		if err != nil {
			return nil, err
		}
		return append([]node.Component{
			node.Named("sender", sender.Run),
			node.Named("loop", loop.Run),
			sqlqueue.Pruner{Queue: q, Every: pruneEvery, Log: logger},
		}, receivers...), nil
	}
}
