// Package node runs a node's roles: it starts the components of each role
// and stops them together (docs/spec.md, section 5.1). What the components
// are and how they connect is decided in the config package (ADR-0028).
package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// Component is a long-running part of a node: Run works until ctx ends,
// then returns nil. An error from Run stops the whole node.
type Component interface {
	Name() string
	Run(ctx context.Context) error
}

// Run starts the given roles and blocks until ctx is cancelled, then
// returns nil, or ErrRestart when ctx was cancelled with that cause. build
// builds the components of the roles; it is called only when roles include
// Core or Edge. An error building or running a component ends Run with that
// error. Worker has no behaviour yet (docs/spec.md, section 10).
func Run(ctx context.Context, roles []Role, build func() ([]Component, error), logger *slog.Logger) error {
	if len(roles) == 0 {
		return errors.New("node: no roles to run")
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}
	logger.Info("node started", "roles", strings.Join(names, ","))
	defer logger.Info("node stopped")
	err := run(ctx, roles, build)
	if errors.Is(err, ErrRestart) {
		logger.Info("node: restart requested; stopping for the supervisor to start it again")
	}
	return err
}

// run runs the roles until ctx ends; see Run.
func run(ctx context.Context, roles []Role, build func() ([]Component, error)) error {
	if !slices.Contains(roles, Core) && !slices.Contains(roles, Edge) {
		<-ctx.Done()
		return stopped(ctx)
	}
	components, err := build()
	if err == nil {
		err = RunAll(ctx, components)
	}
	if err != nil && ctx.Err() == nil {
		return err
	}
	return stopped(ctx) // a stop was asked for; errors on the way out do not matter
}

// RunAll runs the components until ctx ends or one of them fails; then it
// stops the rest and returns the first error, prefixed with the
// component's name. A component that returns nil before ctx ends just
// stops.
func RunAll(ctx context.Context, components []Component) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, c := range components {
		wg.Go(func() {
			err := c.Run(ctx)
			if err != nil && ctx.Err() == nil {
				once.Do(func() { firstErr = fmt.Errorf("%s: %w", c.Name(), err) })
				cancel()
			}
		})
	}
	wg.Wait()
	return firstErr
}

// Named makes a component of a name and a run function, for types such as
// the outbox sender and the turn loop that only have Run.
func Named(name string, run func(ctx context.Context) error) Component { return named{name, run} }

type named struct {
	name string
	run  func(ctx context.Context) error
}

func (n named) Name() string                  { return n.name }
func (n named) Run(ctx context.Context) error { return n.run(ctx) }
