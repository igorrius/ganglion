package config

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/restart"
	"github.com/igorrius/ganglion/texts"
	"github.com/samber/do/v2"
)

// RestartNote provides the record of a restart asked from the chat
// (#238): restart.json beside the database, or nothing for a node whose
// database is in memory.
func RestartNote() do.Provider[restart.Asker] {
	return func(i do.Injector) (restart.Asker, error) {
		if n, ok := restartNote(i); ok {
			return n, nil
		}
		return restart.NoAsker{}, nil
	}
}

// restartNote is the record's file, when the node keeps its database in
// a file.
func restartNote(i do.Injector) (*restart.Note, bool) {
	b := do.MustInvoke[Bootstrap](i)
	if b.DB == "" || b.DB == ":memory:" {
		return nil, false
	}
	return restart.NewNote(filepath.Join(filepath.Dir(b.DB), restart.FileName), time.Time(do.MustInvoke[ProcessStart](i)), time.Now), true
}

// RestartBack provides the component that says the node is back after a
// restart asked from the chat, naming the changes from the chat that took
// effect with it. Only the agent records a restart, so a node without one
// has none.
func RestartBack() do.Provider[Restarts] {
	return func(i do.Injector) (Restarts, error) {
		n, ok := restartNote(i)
		if !ok || !agentOn(do.MustInvoke[Bootstrap](i)) {
			return nil, nil
		}
		history := do.MustInvoke[confighist.History](i)
		applied := func(ctx context.Context, since, until time.Time) ([]string, error) {
			return appliedFromChat(ctx, history, since, until)
		}
		return Restarts{restart.NewBack(n, do.MustInvoke[*sqlqueue.Queue](i).Enqueue, do.MustInvoke[*language.Store](i).Resolve,
			applied, do.MustInvoke[*texts.Catalog](i), time.Now, do.MustInvoke[*slog.Logger](i))}, nil
	}
}

// Restarts are the components that speak after a restart.
type Restarts []node.Component

// historyScan is how many recorded changes are looked at for those that
// took effect with a restart.
const historyScan = 50

// appliedFromChat are the reasons of config.propose and config.rollback
// changes recorded between since and until, oldest first.
func appliedFromChat(ctx context.Context, h confighist.History, since, until time.Time) ([]string, error) {
	cs, err := h.Changes(ctx, historyScan)
	if errors.Is(err, confighist.ErrNoHistory) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cs {
		fromChat := c.Source == confighist.SourcePropose || c.Source == confighist.SourceRollback
		// Commit times are whole seconds.
		if fromChat && !c.When.Before(since.Truncate(time.Second)) && !c.When.After(until) {
			out = append([]string{c.Reason}, out...)
		}
	}
	return out, nil
}
