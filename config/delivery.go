package config

import (
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/intake"
	bgjobs "github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/outbox"
	"github.com/igorrius/ganglion/publichttp"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/transport/telegram/hookrecord"
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
			telegram.WithReplyMode(telegram.ReplyMode(b.TelegramReply)), telegram.WithRichMessages(b.TelegramRich), telegram.WithCommands(do.MustInvoke[[]transport.Command](i)),
			telegram.WithHealth(do.MustInvoke[*telegram.Health](i)), telegram.WithTopics(do.MustInvoke[groups.Topics](i).Seen),
			telegram.WithWebhook(b.PublicURL, do.MustInvoke[*publichttp.Listener](i).Reaches),
			telegram.WithWebhookRecord(do.MustInvoke[*hookrecord.Store](i)), telegram.WithIntake(do.MustInvoke[*intake.Registry](i).Take))
	}
}

// WebhookRecord provides the node's record of the Telegram webhook it set.
func WebhookRecord() do.Provider[*hookrecord.Store] {
	return func(i do.Injector) (*hookrecord.Store, error) {
		return hookrecord.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now)
	}
}

// PublicListener provides the node's public listener (R-CFG-11). It
// listens only as a component, which Listeners adds when http.addr is on.
func PublicListener() do.Provider[*publichttp.Listener] {
	return func(i do.Injector) (*publichttp.Listener, error) {
		return publichttp.New(publichttp.DefaultConfig(do.MustInvoke[Bootstrap](i).HTTPAddr), do.MustInvoke[*slog.Logger](i), time.After)
	}
}

// Listeners are the components that listen for the outside world.
type Listeners []node.Component

// PublicListeners provides the listeners that are on.
func PublicListeners() do.Provider[Listeners] {
	return choose[Listeners](httpOn, listenersHTTP, listenersNone)
}

// HTTPListeners provides the public listener as a component.
func HTTPListeners() do.Provider[Listeners] {
	return func(i do.Injector) (Listeners, error) {
		return Listeners{do.MustInvoke[*publichttp.Listener](i)}, nil
	}
}

// TelegramHealth provides what /status shows of Telegram's polling, kept apart
// from the transport so the agent's sources need not wait for it.
func TelegramHealth() do.Provider[*telegram.Health] {
	return func(do.Injector) (*telegram.Health, error) { return telegram.NewHealth(time.Now), nil }
}

// CommandHints provides the commands the transports list for people.
func CommandHints() do.Provider[[]transport.Command] {
	return choose[[]transport.Command](agentOn, commandsAgent, commandsNone)
}

// AgentCommandHints provides the agent's command table as the hints.
func AgentCommandHints() do.Provider[[]transport.Command] {
	return func(i do.Injector) ([]transport.Command, error) {
		return do.MustInvoke[*agent.Agent](i).Commands(), nil
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

// Status provides who hears the stages of a person's message.
func Status() do.Provider[agent.StatusFunc] {
	return choose[agent.StatusFunc](telegramOn, statusTelegram, statusNone)
}

// TelegramStatus provides Telegram's reactions as what hears the stages.
func TelegramStatus() do.Provider[agent.StatusFunc] {
	return func(i do.Injector) (agent.StatusFunc, error) {
		return do.MustInvoke[*telegram.Reactor](i).Report, nil
	}
}

// Reactor provides Telegram's status reactions; a threshold of 0 keeps it idle.
func Reactor() do.Provider[*telegram.Reactor] {
	return func(i do.Injector) (*telegram.Reactor, error) {
		b := do.MustInvoke[Bootstrap](i)
		cfg := telegram.DefaultReactorConfig()
		cfg.After, cfg.Dwell = b.ReactionAfter, b.ReactionDwell
		if !reactionsOn(b) {
			cfg.After = 0
		}
		return telegram.NewReactor(b.BotToken, b.BotAPIURL, cfg, do.MustInvoke[*slog.Logger](i)), nil
	}
}

// reactionsOn reports whether a Reactor runs: only the agent reports when a turn
// ends, so the echo bot would leave its messages marked.
func reactionsOn(b Bootstrap) bool { return telegramOn(b) && agentOn(b) }

// Reactions are the components that show status, when Telegram and the agent
// are on.
type Reactions []node.Component

// ReactionComponents provides the reaction component that is on.
func ReactionComponents() do.Provider[Reactions] {
	return choose[Reactions](reactionsOn, reactionsTelegram, reactionsNone)
}

// TelegramReactions provides the Reactor as a component.
func TelegramReactions() do.Provider[Reactions] {
	return func(i do.Injector) (Reactions, error) {
		return Reactions{node.Named("reactions", do.MustInvoke[*telegram.Reactor](i).Run)}, nil
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
			core.WithTurnTimeout(do.MustInvoke[Bootstrap](i).Timeouts.Handler),
			core.WithMaxAttempts(core.DefaultMaxAttempts, do.MustInvoke[core.GiveUp](i))), nil
	}
}

// Inbound are the components that bring messages into the inbox.
type Inbound []node.Component

// Receivers provides the inbound components that are on: Telegram by
// polling or by the webhook, as telegram.updates says.
func Receivers() do.Provider[Inbound] {
	return func(i do.Injector) (Inbound, error) {
		b, name := do.MustInvoke[Bootstrap](i), inboundNone
		switch {
		case telegramOn(b) && b.TelegramWebhook:
			name = inboundWebhook
		case telegramOn(b):
			name = inboundTelegram
		}
		return do.InvokeNamed[Inbound](i, name)
	}
}

// TelegramReceivers provides Telegram polling behind the access list.
func TelegramReceivers() do.Provider[Inbound] {
	return func(i do.Injector) (Inbound, error) { return Inbound{telegramPoller(i)}, nil }
}

// TelegramWebhookReceivers provides the Telegram webhook behind the access
// list, with its route on the public listener.
func TelegramWebhookReceivers() do.Provider[Inbound] {
	return func(i do.Injector) (Inbound, error) {
		p := telegramPoller(i)
		do.MustInvoke[*publichttp.Listener](i).Handle(telegram.WebhookPath, p.Transport.WebhookHandler())
		return Inbound{node.Webhook{Poller: p}}, nil
	}
}

// telegramPoller is Telegram's receiving side behind the access list.
func telegramPoller(i do.Injector) node.Poller {
	q, logger := do.MustInvoke[*sqlqueue.Queue](i), do.MustInvoke[*slog.Logger](i)
	return node.Poller{
		Transport: do.MustInvoke[*telegram.Transport](i),
		Sink:      do.MustInvoke[*access.List](i).Filter(do.MustInvoke[*telegram.Reactor](i).Noting(q.Put), do.MustInvoke[access.Groups](i), logger),
		Admins:    do.MustInvoke[alert.Admins](i),
		Enqueue:   q.Enqueue,
		Started:   time.Time(do.MustInvoke[ProcessStart](i)),
		Log:       logger,
	}
}

// Housekeeping are the components that clean up after the parts that are
// on.
type Housekeeping []node.Component

// Housekeepers provides the cleanup components that are on.
func Housekeepers() do.Provider[Housekeeping] {
	return choose[Housekeeping](agentOn, housekeepingAgent, housekeepingNone)
}

// AgentHousekeepers provides the daily prune of the tool-call journal, the
// expiry of unanswered confirmations, the janitor, the translator, the
// runner of host commands, which lives where the agent does, the
// reconcile of the cells at start and every five minutes, and the memory
// review's timer while the review runs.
func AgentHousekeepers() do.Provider[Housekeeping] {
	return func(i do.Injector) (Housekeeping, error) {
		logger := do.MustInvoke[*slog.Logger](i)
		hk := Housekeeping{
			journal.Pruner{Journal: do.MustInvoke[*journal.Journal](i), Every: pruneEvery, Log: logger},
			pending.NewExpirer(do.MustInvoke[*pending.Store](i), do.MustInvoke[*sqlqueue.Queue](i).Put, logger),
			do.MustInvoke[*host.Janitor](i),
			do.MustInvoke[*host.Runner](i),
			do.MustInvoke[*bgjobs.Registry](i),
			do.MustInvoke[*builtin.BuildQueue](i),
			node.Named("translator", do.MustInvoke[*language.Translator](i).Run),
			cells.Reconciler{Service: do.MustInvoke[*cells.Service](i), Every: cells.ReconcileEvery, Log: logger, After: time.After},
		}
		if reviewOn(i) {
			hk = append(hk, do.MustInvoke[*agent.ReviewTimer](i))
		}
		return hk, nil
	}
}

// Schedulers are the components that run jobs at their times.
type Schedulers []node.Component

// JobSchedulers provides the schedulers that are on.
func JobSchedulers() do.Provider[Schedulers] {
	return choose[Schedulers](agentOn, schedulersAgent, schedulersNone)
}

// AgentSchedulers provides the scheduler of the agent's reminders and
// tasks.
func AgentSchedulers() do.Provider[Schedulers] {
	return func(i do.Injector) (Schedulers, error) {
		return Schedulers{do.MustInvoke[*schedule.Scheduler](i)}, nil
	}
}

// CoreComponents provides the core role's components: the sender, the
// turn loop, the pruners, the schedulers, the public listener and the
// inbound components.
func CoreComponents() do.Provider[[]node.Component] {
	return func(i do.Injector) ([]node.Component, error) {
		q := do.MustInvoke[*sqlqueue.Queue](i)
		comps := []node.Component{
			node.Named("sender", do.MustInvoke[*outbox.Sender](i).Run),
			node.Named("loop", do.MustInvoke[*core.Loop](i).Run),
			do.MustInvoke[*intake.Registry](i),
			sqlqueue.Pruner{Queue: q, Every: pruneEvery, Log: do.MustInvoke[*slog.Logger](i)},
		}
		comps = append(comps, do.MustInvoke[Housekeeping](i)...)
		comps = append(comps, do.MustInvoke[Schedulers](i)...)
		comps = append(comps, do.MustInvoke[Reactions](i)...)
		comps = append(comps, do.MustInvoke[Listeners](i)...)
		comps = append(comps, do.MustInvoke[Updates](i)...)
		comps = append(comps, do.MustInvoke[Restarts](i)...)
		comps = append(comps, do.MustInvoke[UpdateChecks](i)...)
		comps = append(comps, do.MustInvoke[Backups](i)...)
		return append(comps, do.MustInvoke[Inbound](i)...), nil
	}
}
