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
		return alert.Admins{List: do.MustInvoke[*access.List](i).Admins, Private: telegram.PrivateConv}, nil
	}
}

// Telegram provides the Telegram transport.
func Telegram() do.Provider[*telegram.Transport] {
	return func(i do.Injector) (*telegram.Transport, error) {
		b := do.MustInvoke[Bootstrap](i)
		return telegram.New(b.BotToken, telegram.WithBaseURL(b.BotAPIURL), telegram.WithLogger(do.MustInvoke[*slog.Logger](i)),
			telegram.WithReplyMode(telegram.ReplyMode(b.TelegramReply)))
	}
}

// Transports provides the transports that are on.
func Transports() do.Provider[[]transport.Transport] {
	return choose[[]transport.Transport](telegramOn, transportsTelegram, transportsNone)
}

// TelegramTransports provides Telegram as the only transport.
func TelegramTransports() do.Provider[[]transport.Transport] {
	return func(i do.Injector) ([]transport.Transport, error) {
		return []transport.Transport{do.MustInvoke[*telegram.Transport](i)}, nil
	}
}

// Sender provides the outbox sender with the transports that are on.
func Sender() do.Provider[*outbox.Sender] {
	return func(i do.Injector) (*outbox.Sender, error) {
		q := do.MustInvoke[*sqlqueue.Queue](i)
		s := outbox.NewSender(q, q.OutboxReady(), do.MustInvoke[*slog.Logger](i))
		for _, t := range do.MustInvoke[[]transport.Transport](i) {
			s.Register(t)
		}
		return s, nil
	}
}

// TurnHook provides what runs around a turn.
func TurnHook() do.Provider[core.TurnHook] {
	return choose[core.TurnHook](telegramOn, turnHookTyping, turnHookNone)
}

// TypingHook provides Telegram's "typing…" as the turn hook.
func TypingHook() do.Provider[core.TurnHook] {
	return func(i do.Injector) (core.TurnHook, error) {
		return do.MustInvoke[*telegram.Transport](i).TypingHook, nil
	}
}

// Loop provides the turn loop over the handler.
func Loop() do.Provider[*core.Loop] {
	return func(i do.Injector) (*core.Loop, error) {
		q := do.MustInvoke[*sqlqueue.Queue](i)
		return core.NewLoop(q, q.InboxReady(), do.MustInvoke[core.Handler](i), do.MustInvoke[*slog.Logger](i),
			core.WithTurnHook(do.MustInvoke[core.TurnHook](i)),
			core.WithMaxAttempts(core.DefaultMaxAttempts, do.MustInvoke[core.GiveUp](i))), nil
	}
}

// Inbound are the components that bring messages into the inbox.
type Inbound []node.Component

// Receivers provides the inbound components that are on.
func Receivers() do.Provider[Inbound] {
	return choose[Inbound](telegramOn, inboundTelegram, inboundNone)
}

// TelegramReceivers provides Telegram polling behind the access list.
func TelegramReceivers() do.Provider[Inbound] {
	return func(i do.Injector) (Inbound, error) {
		q, logger := do.MustInvoke[*sqlqueue.Queue](i), do.MustInvoke[*slog.Logger](i)
		return Inbound{node.Poller{
			Transport: do.MustInvoke[*telegram.Transport](i),
			Sink:      do.MustInvoke[*access.List](i).Filter(q.Put, logger),
			Admins:    do.MustInvoke[alert.Admins](i),
			Enqueue:   q.Enqueue,
			Started:   time.Time(do.MustInvoke[ProcessStart](i)),
			Log:       logger,
		}}, nil
	}
}

// CoreComponents provides the core role's components: the sender, the
// turn loop, the pruner and the inbound components.
func CoreComponents() do.Provider[[]node.Component] {
	return func(i do.Injector) ([]node.Component, error) {
		q := do.MustInvoke[*sqlqueue.Queue](i)
		return append([]node.Component{
			node.Named("sender", do.MustInvoke[*outbox.Sender](i).Run),
			node.Named("loop", do.MustInvoke[*core.Loop](i).Run),
			sqlqueue.Pruner{Queue: q, Every: pruneEvery, Log: do.MustInvoke[*slog.Logger](i)},
		}, do.MustInvoke[Inbound](i)...), nil
	}
}
