package agent

import (
	"context"

	"github.com/igorrius/ganglion/texts"
)

// RestartFunc asks the node to stop so its supervisor starts it again.
type RestartFunc func()

// restart ends the node so its supervisor starts it again (R-CFG-6): the
// settings outside modules apply at start. Admin only. The reply is queued
// with the turn's result before the node stops; the outbox survives the
// restart, so it is delivered after it.
func (a *Agent) restart(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	t.reply(a.say(ctx, t, texts.ReplyRestarting, nil))
	a.d.Restart()
	return nil
}
