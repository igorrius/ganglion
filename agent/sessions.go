package agent

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/event"
)

// Archive keeps ended sessions (task 7).
type Archive struct{}

// sessions handles session commands and the idle reset before a turn
// (task 7). handled means the event needs no model call.
func (a *Agent) sessions(_ context.Context, _ event.Event, st State, _ event.Message, _ time.Time, _ *turnRecord,
	_ func(string) event.Outgoing) (State, []event.Outgoing, bool) {
	return st, nil, false
}
