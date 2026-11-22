package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/transcript"
	bg "github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/turns"
)

// Builds reads the module builds that ran in the background and hears when a
// person writes in a conversation, which lets its modules be built again
// (builtin.BuildQueue, MaxBuildsInARow).
type Builds interface {
	Outcome(ctx context.Context, key string) (builtin.BuildOutcome, error)
	OwnerSpoke(conv string)
}

// NoBuilds is the Builds of a node without module builds.
type NoBuilds struct{}

// Outcome implements Builds.
func (NoBuilds) Outcome(context.Context, string) (builtin.BuildOutcome, error) {
	return builtin.BuildOutcome{}, builtin.ErrNoBuildJob
}

// OwnerSpoke implements Builds.
func (NoBuilds) OwnerSpoke(string) {}

// ownerSpoke tells the build queue that a person wrote in the conversation:
// the count of builds in a row starts afresh (owner, 2026-11-10). A build's
// own turn, a press or a scheduled run is not a person's message.
func (a *Agent) ownerSpoke(_ context.Context, t *turn) (bool, error) {
	if t.event.Kind == event.KindMessage {
		a.d.Builds.OwnerSpoke(t.event.Conv)
	}
	return false, nil
}

// bgOpen are the states of a job that has not ended.
var bgOpen = []bg.State{bg.StateQueued, bg.StateRunning}

// varModule is the texts' variable that names a module.
const varModule = "module"

// buildFinishedPrefix starts the user-role note of a build's answer:
// "[build finished] <module>: <answer>".
const buildFinishedPrefix = "[build finished] "

// moduleBuilt resumes the conversation of a module build that ended in the
// background (R-RUN-9, ADR-0069), as commandDone does a host command's: the
// build's answer joins the history, as outside data only when the build
// says so (the provenance rule, R-RUN-17), and the model goes on with its
// tools at the depth of the call that asked for the build (owner,
// 2026-11-10: the build is the node's work, not the agent calling itself;
// MaxBuildsInARow ends a loop that does not converge), to fix the source and
// build again or to install.
// A build the session no longer waits for gets one fixed line and no model
// turn; a build of another conversation does nothing here.
func (a *Agent) moduleBuilt(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	o, err := a.d.Builds.Outcome(ctx, t.built.Key)
	if errors.Is(err, builtin.ErrNoBuildJob) {
		a.log.Warn("agent: the end of an unknown build", "key", t.built.Key, "conv", t.event.Conv)
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: build %s: %w", t.built.Key, err)
	}
	if o.Conv != t.event.Conv || o.State == "" || slices.Contains(bgOpen, o.State) {
		a.log.Warn("agent: the end of a build that is not this conversation's or has not ended", "key", o.Key, "conv", t.event.Conv)
		return nil
	}
	t.msg.From, t.record.Principal = o.Principal, o.Principal
	if done, err := a.commandAsTask(ctx, t); err != nil || done {
		return err
	}
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !slices.Contains(t.state.Commands, o.Key) {
		t.reply(a.say(ctx, t, texts.ReplyBuildLate, texts.Vars{varModule: text.CodeSpan(o.Module)}))
		return nil
	}
	return a.continueBuild(ctx, t, o)
}

// continueBuild takes the ended build off the session's list and goes on
// with its answer. Over the daily limit the note joins the history without a
// model turn, as a command's does.
func (a *Agent) continueBuild(ctx context.Context, t *turn, o builtin.BuildOutcome) error {
	t.state.Commands = slices.DeleteFunc(slices.Clone(t.state.Commands), func(k string) bool { return k == o.Key })
	answer := o.Answer
	if o.External {
		t.state.Tainted = true
		answer = tool.WrapExternal(builtin.ModuleBuildTool, answer)
	}
	note := provider.Message{Role: provider.RoleUser, Content: buildFinishedPrefix + o.Module + ": " + transcript.Cap(answer)}
	if over, _ := a.limits(ctx, t); over {
		t.state.Messages, t.state.Last = append(t.state.Messages, note), t.now
		return nil
	}
	t.record.Outcome = turns.OutcomeOK
	t.depth = o.Depth
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}

// buildToBeTold reports whether key is a build whose end is still to be
// told: one the queue keeps a record of. Its module.built event takes it
// off the list.
func (a *Agent) buildToBeTold(ctx context.Context, key string) (bool, error) {
	_, err := a.d.Builds.Outcome(ctx, key)
	switch {
	case errors.Is(err, builtin.ErrNoBuildJob):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("agent: build %s: %w", key, err)
	}
	return true, nil
}

// quietForBuild reports whether a turn's text is kept out of the chat
// (ADR-0112): the session waits for a build of its own that is queued or
// running, and the turn is not a person's message, or it is the one that
// started a build (awaited is the list before the turn). The text joins the
// history; the build's note resumes the conversation, and the build's card
// is the owner's sign of life. A question the turn asked goes out as its
// own card, not as this text, and a person who asks while a build runs gets
// an answer.
func (a *Agent) quietForBuild(ctx context.Context, t *turn, awaited []string) (bool, error) {
	started := false
	for _, k := range t.state.Commands {
		if strings.HasPrefix(k, builtin.BuildKeyPrefix) && !slices.Contains(awaited, k) {
			started = true
		}
	}
	if t.event.Kind == event.KindMessage && !started {
		return false, nil
	}
	for _, k := range t.state.Commands {
		if !strings.HasPrefix(k, builtin.BuildKeyPrefix) {
			continue
		}
		running, err := a.buildRunning(ctx, k)
		if err != nil {
			return false, err
		}
		if running {
			a.log.Debug("agent: a turn's text is kept quiet while a build runs", "conv", t.event.Conv, "build", k)
			return true, nil
		}
	}
	return false, nil
}

// buildRunning reports whether key is a build that runs or waits.
func (a *Agent) buildRunning(ctx context.Context, key string) (bool, error) {
	o, err := a.d.Builds.Outcome(ctx, key)
	switch {
	case errors.Is(err, builtin.ErrNoBuildJob):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("agent: build %s: %w", key, err)
	}
	return slices.Contains(bgOpen, o.State), nil
}
