// Package config assembles a node: one samber/do provider per component,
// one do.Package per role, and Run to start the result (ADR-0028). It is
// the only package that connects concrete implementations; the command
// line fills its Bootstrap (ADR-0029).
package config

import (
	"context"
	"log/slog"
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
	do.Lazy(Telegram()), do.Lazy(Sender()), do.Lazy(TurnHook()),
	do.Lazy(Turns()), do.Lazy(Archive()), do.Lazy(Providers()), do.Lazy(Router()), do.Lazy(Agent()),
	do.Lazy(Handler()), do.Lazy(GiveUpReplies()), do.Lazy(Loop()), do.Lazy(CoreComponents()),
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
