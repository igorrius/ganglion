package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// Commands reads the outcomes of host commands (host.Runner).
type Commands interface {
	Get(ctx context.Context, key string) (host.Job, error)
	Running(ctx context.Context, keys []string) ([]string, error)
}

// NoCommands is the Commands of a node without host commands.
type NoCommands struct{}

// Get implements Commands.
func (NoCommands) Get(context.Context, string) (host.Job, error) { return host.Job{}, host.ErrNoJob }

// Running implements Commands.
func (NoCommands) Running(context.Context, []string) ([]string, error) { return nil, nil }

// commandFinishedPrefix starts the user-role note of a background command's
// outcome: "[command finished] <argv>: <outcome as data>".
const commandFinishedPrefix = "[command finished] "

// hostExecTool is the tool whose outcome a command.done event carries.
const hostExecTool = "host.exec"

// commandDone resumes the conversation of a host command that ended in the
// background (R-CMD-4), as a press resumes a parked call (ADR-0032): the
// outcome joins the history as outside data and the model goes on, with its
// tools, one level deeper. A command the session no longer waits for (it
// ended, or /new came) gets one fixed line and no model turn, and its output
// stays out of the new session. A command of another conversation does
// nothing here: its line and its output belong to that one.
func (a *Agent) commandDone(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	j, err := a.d.Commands.Get(ctx, t.command.Key)
	if errors.Is(err, host.ErrNoJob) {
		a.log.Warn("agent: the end of an unknown command", "key", t.command.Key, "conv", t.event.Conv)
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: command %s: %w", t.command.Key, err)
	}
	if j.Conv != t.event.Conv {
		a.log.Warn("agent: the end of another conversation's command", "key", j.Key, "conv", t.event.Conv)
		return nil
	}
	t.msg.From, t.record.Principal = j.Principal, j.Principal
	if done, err := a.commandAsTask(ctx, t); err != nil || done {
		return err
	}
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !slices.Contains(t.state.Commands, j.Key) {
		t.reply(a.say(ctx, t, texts.ReplyCommandLate, texts.Vars{"command": text.CodeSpan(hostcmd.Show(j.Argv))}))
		return nil
	}
	return a.continueCommand(ctx, t, j)
}

// continueCommand takes the ended command off the session's list and goes
// on with its outcome as outside data. Over the daily limit the note joins
// the history without a model turn, as a confirmation's does.
func (a *Agent) continueCommand(ctx context.Context, t *turn, j host.Job) error {
	t.state.Commands = slices.DeleteFunc(slices.Clone(t.state.Commands), func(k string) bool { return k == j.Key })
	t.state.Tainted = true
	if err := a.d.Plans.Ran(ctx, t.event.Conv, tool.Command{Argv: j.Argv, Dir: j.Dir}, j.OK()); err != nil {
		a.log.Warn("agent: the plan's step was not ticked", "conv", t.event.Conv, "key", j.Key, "err", err)
	}
	note := provider.Message{Role: provider.RoleUser, Content: commandFinishedPrefix + hostcmd.Show(j.Argv) + ": " +
		transcript.Cap(tool.WrapExternal(hostExecTool, j.Text))}
	if over, _ := a.limits(ctx, t); over {
		t.state.Messages, t.state.Last = append(t.state.Messages, note), t.now
		return nil
	}
	t.record.Outcome = turns.OutcomeOK
	t.depth = j.Depth + 1
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}

// commandAsTask makes a command's turn in a task's conversation a turn of that
// task, under its grant (ruling 7 of the M3d plan); done when the task is gone.
func (a *Agent) commandAsTask(ctx context.Context, t *turn) (bool, error) {
	id, ok := strings.CutPrefix(t.event.Conv, tool.TaskConvPrefix)
	if !ok {
		return false, nil
	}
	j, err := a.d.Jobs.Get(ctx, id)
	switch {
	case errors.Is(err, schedule.ErrNotFound) || err == nil && j.Deleted:
		a.log.Info("agent: a command of a task that is gone", "task", id)
		return true, nil
	case err != nil:
		return false, fmt.Errorf("agent: task %s: %w", id, err)
	}
	return false, a.asTask(ctx, t, j)
}

// dropStaleCommands keeps the listed commands and builds whose end is still
// to be told: those running, and those that ended in the background, whose
// command.done or module.built event resumes them and takes them off the
// list (it may still be on its way to the inbox). A row that is gone, or a
// command whose call was told its end, is dropped.
func (a *Agent) dropStaleCommands(ctx context.Context, t *turn) error {
	if len(t.state.Commands) == 0 {
		return nil
	}
	running, err := a.d.Commands.Running(ctx, t.state.Commands)
	if err != nil {
		return fmt.Errorf("agent: running commands: %w", err)
	}
	var kept []string
	for _, k := range t.state.Commands {
		ok := slices.Contains(running, k)
		if !ok {
			if ok, err = a.toBeTold(ctx, k); err != nil {
				return err
			}
		}
		if ok {
			kept = append(kept, k)
		}
	}
	t.state.Commands = kept
	return nil
}

// toBeTold reports whether key's command ended in the background, so its
// command.done event is still to come or to be handled, or whether key is a
// build whose module.built is (buildToBeTold).
func (a *Agent) toBeTold(ctx context.Context, key string) (bool, error) {
	j, err := a.d.Commands.Get(ctx, key)
	switch {
	case errors.Is(err, host.ErrNoJob):
		return a.buildToBeTold(ctx, key)
	case err != nil:
		return false, fmt.Errorf("agent: command %s: %w", key, err)
	}
	return j.Background, nil
}

// stillRunning keeps the keys among keys whose command or build still runs
// or waits: a session /resume brings back keeps only those (an ended one was
// told as late).
func (a *Agent) stillRunning(ctx context.Context, keys []string) ([]string, error) {
	running, err := a.d.Commands.Running(ctx, keys)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		ok := slices.Contains(running, k)
		if !ok {
			if ok, err = a.buildRunning(ctx, k); err != nil {
				return nil, err
			}
		}
		if ok {
			out = append(out, k)
		}
	}
	return out, nil
}
