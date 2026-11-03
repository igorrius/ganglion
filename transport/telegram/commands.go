package telegram

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/igorrius/ganglion/transport"
)

// commandsTimeout bounds one setMyCommands call at start.
const commandsTimeout = 10 * time.Second

// botCommand is a Bot API BotCommand.
type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// registerCommands lists the transport's commands in Telegram's command
// hints, once for the default language (no language_code) and once for
// each other language a command is described in (rulings R43, R45).
// setMyCommands replaces the list, so doing it at every start is
// idempotent. A failure is logged and does not stop the transport: the
// hints are a convenience.
func (t *Transport) registerCommands(ctx context.Context) {
	if len(t.commands) == 0 {
		return
	}
	for _, lang := range languages(t.commands) {
		cctx, cancel := context.WithTimeout(ctx, commandsTimeout)
		err := t.c.setMyCommands(cctx, hintsIn(t.commands, lang), string(lang))
		cancel()
		if err != nil && ctx.Err() == nil {
			t.log.Warn("telegram: command hints not set", "language", string(lang), "err", err)
		}
	}
}

// languages are the default and every language a command is described
// in, the default first, then in order.
func languages(cmds []transport.Command) []transport.Lang {
	set := map[transport.Lang]bool{transport.LangDefault: true}
	for _, c := range cmds {
		for l := range c.Description {
			set[l] = true
		}
	}
	return slices.Sorted(maps.Keys(set)) // "" sorts first
}

// hintsIn are the commands with their descriptions in lang, or the
// default description where lang has none.
func hintsIn(cmds []transport.Command, lang transport.Lang) []botCommand {
	out := make([]botCommand, 0, len(cmds))
	for _, c := range cmds {
		d, ok := c.Description[lang]
		if !ok {
			d = c.Description[transport.LangDefault]
		}
		out = append(out, botCommand{Command: c.Name, Description: d})
	}
	return out
}
