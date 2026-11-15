// Package config assembles a node: one samber/do provider per component,
// one do.Package per role, and Run to start the result (ADR-0028). It
// holds dependency logic only: what each component needs, which
// implementation a setting selects, and the container's lifecycle. Every
// behaviour lives in the package that owns it; the command line fills
// Bootstrap (ADR-0029).
package config

import (
	"context"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/transport"
	"github.com/samber/do/v2"
)

// Startup is the context for start-time work such as migrations; running
// components get their own context from node.Run.
type Startup struct{ context.Context }

// ProcessStart is when this process started.
type ProcessStart time.Time

// Core is the core role: storage, delivery, the turn loop and the agent.
var Core = do.Package(
	do.Lazy(Lock()), do.Lazy(DB()), do.Lazy(Queue()), do.Lazy(Access()), do.Lazy(Languages()), do.Lazy(Texts()), do.Lazy(Admins()), do.Lazy(GroupStore()), do.Lazy(Vault()), do.Lazy(GroupsFile()), do.Lazy(AccessGroups()), do.Lazy(GroupActions()), do.Lazy(GroupTopics()), do.Lazy(Destinations()), do.Lazy(Routing()),
	do.Lazy(Turns()), do.Lazy(Archive()), do.Lazy(Journal()), do.Lazy(Cells()), do.Lazy(Pending()), do.Lazy(Jobs()), do.Lazy(Scheduler()), do.Lazy(Scheduling()), do.Lazy(Convs()), do.Lazy(Guard()), do.Lazy(Workspace()), do.Lazy(Memory()), do.Lazy(Remember()), do.Lazy(ReviewDue()), do.Lazy(ReviewTimer()), do.Lazy(ModuleTools()), do.Lazy(Modules()), do.Lazy(ModuleAdmin()), do.Lazy(ModuleHost()), do.Lazy(HTML2MD()), do.Lazy(Fetcher()), do.Lazy(Read()), do.Lazy(Diagnostics()), do.Lazy(Skills()), do.Lazy(Plans()), do.Lazy(Tools()), do.Lazy(Policy()), do.Lazy(Asker()), do.Lazy(IsAdmin()), do.Lazy(Providers()), do.Lazy(Router()), do.Lazy(Sandbox()), do.Lazy(Builder()), do.Lazy(Places()), do.Lazy(Janitor()), do.Lazy(Stats()), do.Lazy(BackgroundJobs()), do.Lazy(Builds()), do.Lazy(Runner()), do.Lazy(Switch()), do.Lazy(HostExec()), do.Lazy(StatusSources()), do.Lazy(Agent()),
	do.Lazy(Translator()), do.Lazy(Telegram()), do.Lazy(TelegramHealth()), do.Lazy(WebhookRecord()), do.Lazy(PublicListener()), do.Lazy(Reactor()), do.Lazy(Sender()), do.Lazy(Loop()), do.Lazy(ReleaseStore()), do.Lazy(CoreComponents()),
	// Extension points: the interface, then each implementation under its name.
	do.Lazy(Handler()), do.LazyNamed(handlerAgent, AgentHandler()), do.EagerNamed(handlerEcho, core.Handler(core.EchoHandler)),
	do.Lazy(GiveUp()), do.LazyNamed(giveUpAgent, AgentGiveUp()), do.EagerNamed(giveUpNone, core.GiveUp(core.NoGiveUp)),
	do.Lazy(TurnHook()), do.LazyNamed(turnHookTyping, TypingHook()), do.EagerNamed(turnHookNone, core.TurnHook(core.NoTurnHook)),
	do.Lazy(Status()), do.LazyNamed(statusTelegram, TelegramStatus()), do.EagerNamed(statusNone, agent.StatusFunc(agent.NoStatus)),
	do.Lazy(ReactionComponents()), do.LazyNamed(reactionsTelegram, TelegramReactions()), do.EagerNamed(reactionsNone, Reactions(nil)),
	do.Lazy(Transports()), do.LazyNamed(transportsTelegram, TelegramTransports()), do.EagerNamed(transportsNone, []transport.Transport(nil)),
	do.Lazy(Housekeepers()), do.LazyNamed(housekeepingAgent, AgentHousekeepers()), do.EagerNamed(housekeepingNone, Housekeeping(nil)),
	do.Lazy(JobSchedulers()), do.LazyNamed(schedulersAgent, AgentSchedulers()), do.EagerNamed(schedulersNone, Schedulers(nil)),
	do.Lazy(Receivers()), do.LazyNamed(inboundTelegram, TelegramReceivers()), do.LazyNamed(inboundWebhook, TelegramWebhookReceivers()), do.EagerNamed(inboundNone, Inbound(nil)),
	do.Lazy(PublicListeners()), do.LazyNamed(listenersHTTP, HTTPListeners()), do.EagerNamed(listenersNone, Listeners(nil)),
	do.Lazy(CommandHints()), do.LazyNamed(commandsAgent, AgentCommandHints()), do.EagerNamed(commandsNone, []transport.Command(nil)),
	do.Lazy(TransportWatch()), do.LazyNamed(watchTelegram, TelegramWatch()), do.EagerNamed(watchNone, Watched(nil)),
	do.Lazy(UpdateComponents()), do.LazyNamed(updateWatch, UpdateWatch()), do.EagerNamed(updateNone, Updates(nil)),
	// The search backends; Read picks one, or none.
	do.LazyNamed(searchBrave, BraveSearch()), do.LazyNamed(searchDuckDuckGo, DuckDuckGoSearch()),
)

// New returns a container with the bootstrap values and the role
// packages.
func New(ctx context.Context, b Bootstrap, logger *slog.Logger) *do.RootScope {
	return do.New(func(i do.Injector) {
		do.ProvideValue(i, Startup{ctx})
		do.ProvideValue(i, b)
		do.ProvideValue(i, logger)
		do.ProvideValue(i, ProcessStart(time.Now()))
		// A node that nothing supervises has no restart; Run replaces it.
		do.ProvideValue(i, agent.RestartFunc(func() {}))
	}, Core)
}

// Run builds the node for roles from b, runs it until ctx ends and then
// shuts the container down, closing resources in reverse dependency
// order.
func Run(ctx context.Context, roles []node.Role, b Bootstrap, logger *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	i := New(ctx, b, logger)
	do.OverrideValue(i, agent.RestartFunc(func() { cancel(node.ErrRestart) }))
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rep := i.ShutdownWithContext(sctx); !rep.Succeed {
			logger.Warn("config: shutdown incomplete", "err", rep.Error())
		}
	}()
	return node.Run(ctx, roles, func() ([]node.Component, error) { return do.Invoke[[]node.Component](i) }, logger)
}

// Names of the implementations of the extension points. do names are
// shared by all types, so each one is unique.
const (
	handlerAgent       = "handler.agent"       // the model-driven agent
	handlerEcho        = "handler.echo"        // the M2 echo bot
	giveUpAgent        = "giveup.agent"        // an apology and an admin alert
	giveUpNone         = "giveup.none"         // give up silently
	turnHookTyping     = "turnhook.typing"     // "typing…" in Telegram
	turnHookNone       = "turnhook.none"       // nothing around a turn
	statusTelegram     = "status.telegram"     // Telegram's reactions show the stages
	statusNone         = "status.none"         // nothing shows them
	reactionsTelegram  = "reactions.telegram"  // the Reactor runs
	reactionsNone      = "reactions.none"      // nothing to run
	transportsTelegram = "transports.telegram" // Telegram
	transportsNone     = "transports.none"     // no transport
	housekeepingAgent  = "housekeeping.agent"  // prune the tool-call journal, expire confirmations
	housekeepingNone   = "housekeeping.none"   // nothing to clean up
	searchBrave        = "search.brave"        // the Brave Search API, with BRAVE_API_KEY
	searchDuckDuckGo   = "search.duckduckgo"   // DuckDuckGo's HTML results, no key
	schedulersAgent    = "schedulers.agent"    // the agent's reminders and tasks
	schedulersNone     = "schedulers.none"     // nothing runs on a schedule
	inboundTelegram    = "inbound.telegram"    // Telegram polling
	inboundWebhook     = "inbound.webhook"     // Telegram's webhook on the public listener
	inboundNone        = "inbound.none"        // nothing brings messages in
	listenersHTTP      = "listeners.http"      // the public listener
	listenersNone      = "listeners.none"      // no public entry
	commandsAgent      = "commands.agent"      // the agent's command table as command hints
	commandsNone       = "commands.none"       // no command hints
	watchTelegram      = "watch.telegram"      // Telegram's polling in /status
	watchNone          = "watch.none"          // no transport in /status
	updateWatch        = "update.watch"        // confirm a new version, tell the admins
	updateNone         = "update.none"         // a node that never updated
)

// agentOn reports whether the settings turn the agent on: the file has a
// models section.
func agentOn(b Bootstrap) bool { return b.Agent.Models != nil }

// telegramOn reports whether the settings turn Telegram on: a bot token
// is set.
func telegramOn(b Bootstrap) bool { return b.BotToken != "" }

// httpOn reports whether the settings open the public listener: http.addr
// is an address. A bootstrap made in code that names none opens none.
func httpOn(b Bootstrap) bool { return b.HTTPAddr != "" && b.HTTPAddr != "off" }

// choose provides an extension point as the implementation named ifOn or
// ifOff, by the bootstrap settings; only that implementation is built.
func choose[T any](on func(Bootstrap) bool, ifOn, ifOff string) do.Provider[T] {
	return func(i do.Injector) (T, error) {
		name := ifOff
		if on(do.MustInvoke[Bootstrap](i)) {
			name = ifOn
		}
		return do.InvokeNamed[T](i, name)
	}
}
