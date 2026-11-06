package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/texts"
)

// Modules lists, updates and rolls back the node's modules (R-RUN-8).
type Modules interface {
	List(ctx context.Context) ([]modstore.Version, error)
	Update(ctx context.Context, name string) (modstore.Outcome, error)
	Rollback(ctx context.Context, name string) (modstore.Outcome, error)
}

// NoModules is a node without a module store.
type NoModules struct{}

// List lists nothing.
func (NoModules) List(context.Context) ([]modstore.Version, error) { return nil, nil }

// Update has no module to update.
func (NoModules) Update(context.Context, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, modstore.ErrUnknownModule
}

// Rollback has no module to roll back.
func (NoModules) Rollback(context.Context, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, modstore.ErrUnknownModule
}

const (
	replyModulesUsage = "Usage: /modules, /modules update <name>, /modules rollback <name>"
	replyNoModules    = "No modules."
	modulesNote       = commandPrefix + "the owner ran /modules: "
)

// modules is /modules: the list, or an update or a rollback by name, for
// an admin, without a model. The reply and a [command] note in the
// history say what happened.
func (a *Agent) modules(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	verb, name, _ := strings.Cut(strings.TrimSpace(arg), " ")
	name = strings.TrimSpace(name)
	var (
		text string
		err  error
	)
	switch {
	case verb == "" && name == "":
		text, err = a.listModules(ctx)
	case verb == "update" && name != "":
		text, err = outcomeText(name, func() (modstore.Outcome, error) { return a.d.Modules.Update(ctx, name) })
	case verb == "rollback" && name != "":
		text, err = outcomeText(name, func() (modstore.Outcome, error) { return a.d.Modules.Rollback(ctx, name) })
	default:
		text = replyModulesUsage
	}
	if err != nil {
		return fmt.Errorf("agent: /modules: %w", err)
	}
	t.reply(text)
	t.note(modulesNote + text)
	return nil
}

func (a *Agent) listModules(ctx context.Context) (string, error) {
	vs, err := a.d.Modules.List(ctx)
	if err != nil || len(vs) == 0 {
		return replyNoModules, err
	}
	var (
		order []string
		lines = map[string][]string{}
	)
	for _, v := range vs {
		if _, seen := lines[v.Module]; !seen {
			order = append(order, v.Module)
		}
		var s string
		switch v.State {
		case modstore.StateActive:
			s = v.Version
			if v.Source != modstore.SourceBuiltin {
				s += " (" + v.Source + ")"
			}
		case modstore.StatePrevious:
			s = "previous: " + v.Version
		case modstore.StateFailed:
			s = v.Version + " failed: " + v.Reason
		case modstore.StateRetired:
			continue
		}
		lines[v.Module] = append(lines[v.Module], s)
	}
	var b strings.Builder
	b.WriteString("Modules:")
	for _, m := range order {
		b.WriteString("\n- " + m + ": " + strings.Join(lines[m], "; "))
	}
	return b.String(), nil
}

// outcomeText runs an update or a rollback and says what it did.
func outcomeText(name string, run func() (modstore.Outcome, error)) (string, error) {
	o, err := run()
	if errors.Is(err, modstore.ErrUnknownModule) {
		return "No module named " + name + ".", nil
	}
	if err != nil {
		return "", err
	}
	switch o.Result {
	case modstore.ResultInstalled:
		return fmt.Sprintf("%s updated from %s to %s.", o.Module, o.From, o.To), nil
	case modstore.ResultUpToDate:
		return fmt.Sprintf("%s is up to date (%s).", o.Module, o.From), nil
	case modstore.ResultRolledBack:
		return fmt.Sprintf("%s rolled back from %s to %s.", o.Module, o.From, o.To), nil
	case modstore.ResultNothingToRollBack:
		return fmt.Sprintf("%s has no previous version to roll back to.", o.Module), nil
	case modstore.ResultRefused, modstore.ResultFailedSelfTest:
	}
	return fmt.Sprintf("%s stays on %s: %s.", o.Module, o.From, strings.TrimSuffix(o.Reason, ".")), nil
}
