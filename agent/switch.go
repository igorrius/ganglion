package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/texts"
)

// Switch is the kill switch of host commands (host.Switch): /commands sets it.
type Switch interface {
	On(ctx context.Context) bool
	Set(ctx context.Context, on bool, by string) error
}

// ErrNoSwitch is what NoSwitch answers to Set.
var ErrNoSwitch = errors.New("agent: this node has no kill switch for commands")

// NoSwitch is the Switch of a node with no host commands: off, and it
// refuses to be set.
type NoSwitch struct{}

// On implements Switch.
func (NoSwitch) On(context.Context) bool { return false }

// Set implements Switch.
func (NoSwitch) Set(context.Context, bool, string) error { return ErrNoSwitch }

// commandsSwitch is /commands: an admin turns host commands on or off, or
// reads the state, without a model.
func (a *Agent) commandsSwitch(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case wordOn:
		return a.setCommands(ctx, t, true)
	case wordOff:
		return a.setCommands(ctx, t, false)
	}
	state := texts.ReplyCommandsOff
	if a.d.Switch.On(ctx) {
		state = texts.ReplyCommandsOn
	}
	t.reply(a.say(ctx, t, state, nil) + "\n" + a.say(ctx, t, texts.ReplyCommandsUsage, nil))
	return nil
}

// setCommands sets the switch for the sender and says the new state.
func (a *Agent) setCommands(ctx context.Context, t *turn, on bool) error {
	if err := a.d.Switch.Set(ctx, on, t.msg.From); err != nil {
		return fmt.Errorf("agent: /commands: %w", err)
	}
	state := texts.ReplyCommandsOff
	if on {
		state = texts.ReplyCommandsOn
	}
	text := a.say(ctx, t, state, nil)
	t.reply(text)
	t.note(commandsNote + text)
	return nil
}

// commandsNote starts the history's note that an admin set the switch.
const commandsNote = commandPrefix + "the owner set host commands with /commands: "
