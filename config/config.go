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
	"reflect"
	"time"

	"github.com/igorrius/ganglion/node"
	"github.com/samber/do/v2"
)

// Startup is the context for start-time work such as migrations; running
// components get their own context from node.Run.
type Startup struct{ context.Context }

// ProcessStart is when this process started.
type ProcessStart time.Time

// Core is the core role: storage, delivery, the turn loop and the agent.
var Core = do.Package(
	do.Lazy(Lock()), do.Lazy(DB()), do.Lazy(Queue()), do.Lazy(Access()), do.Lazy(Admins()),
	do.Lazy(Turns()), do.Lazy(Archive()), do.Lazy(Providers()), do.Lazy(Router()), do.Lazy(Agent()),
	do.Lazy(Telegram()), do.Lazy(Sender()), do.Lazy(Loop()), do.Lazy(CoreComponents()),
	// Extension points: the interface, then each implementation.
	do.Lazy(Handler()), impl(implAgent, AgentHandler()), impl(implEcho, EchoHandler()),
	do.Lazy(GiveUp()), impl(implAgent, AgentGiveUp()), impl(implEcho, NoGiveUp()),
	do.Lazy(TurnHook()), impl(implTelegram, TypingHook()), impl(implNone, NoTurnHook()),
	do.Lazy(Transports()), impl(implTelegram, TelegramTransports()), impl(implNone, NoTransports()),
	do.Lazy(Receivers()), impl(implTelegram, TelegramReceivers()), impl(implNone, NoReceivers()),
)

// New returns a container with the bootstrap values and the role
// packages.
func New(ctx context.Context, b Bootstrap, logger *slog.Logger) *do.RootScope {
	return do.New(func(i do.Injector) {
		do.ProvideValue(i, Startup{ctx})
		do.ProvideValue(i, b)
		do.ProvideValue(i, logger)
		do.ProvideValue(i, ProcessStart(time.Now()))
	}, Core)
}

// Run builds the node for roles from b, runs it until ctx ends and then
// shuts the container down, closing resources in reverse dependency
// order.
func Run(ctx context.Context, roles []node.Role, b Bootstrap, logger *slog.Logger) error {
	i := New(ctx, b, logger)
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rep := i.ShutdownWithContext(sctx); !rep.Succeed {
			logger.Warn("config: shutdown incomplete", "err", rep.Error())
		}
	}()
	return node.Run(ctx, roles, func() ([]node.Component, error) { return do.Invoke[[]node.Component](i) }, logger)
}

// Names of the implementations of an extension point.
const (
	implAgent    = "agent"    // the model-driven agent
	implEcho     = "echo"     // the M2 echo bot, when no model catalogue is set
	implTelegram = "telegram" // the Telegram transport, when a bot token is set
	implNone     = "none"     // nothing, when the part is off
)

// agentOrEcho selects the agent when a model catalogue is set.
func agentOrEcho(b Bootstrap) string {
	if b.Agent.Catalog != nil {
		return implAgent
	}
	return implEcho
}

// telegramOrNone selects Telegram when a bot token is set.
func telegramOrNone(b Bootstrap) string {
	if b.BotToken != "" {
		return implTelegram
	}
	return implNone
}

// choose provides an extension point as the implementation that pick
// names for the bootstrap settings; only that implementation is built.
func choose[T any](pick func(Bootstrap) string) do.Provider[T] {
	return func(i do.Injector) (T, error) {
		b, err := do.Invoke[Bootstrap](i)
		if err != nil {
			var zero T
			return zero, err
		}
		return do.InvokeNamed[T](i, implKey[T](pick(b)))
	}
}

// impl registers p as the implementation called name of its extension
// point.
func impl[T any](name string, p do.Provider[T]) func(do.Injector) {
	return do.LazyNamed(implKey[T](name), p)
}

// implKey is the container name of an implementation: do names are shared
// by all types, so the extension point's type is part of it.
func implKey[T any](name string) string {
	return reflect.TypeFor[T]().String() + "#" + name
}

// need invokes a T for a provider, keeping the first error in *err; after
// an error it returns zero values, so a provider checks err once.
func need[T any](i do.Injector, err *error) T {
	var zero T
	if *err != nil {
		return zero
	}
	v, e := do.Invoke[T](i)
	if e != nil {
		*err = e
		return zero
	}
	return v
}

// value provides a fixed implementation.
func value[T any](v T) do.Provider[T] {
	return func(do.Injector) (T, error) { return v, nil }
}
