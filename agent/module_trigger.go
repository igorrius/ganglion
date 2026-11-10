package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/igorrius/ganglion/event"
)

// moduleTrigger runs the listener of a module.trigger event through the
// module host (R-RUN-15, ADR-0065). It is no turn: no model, no message, no
// conversation state, no turn row (ruling 4 of M4c). Only the node's
// triggers count: one from another source, or one the agent cannot read, is
// logged and acknowledged, so a transport never starts a module. A run that
// failed is acknowledged too, since a failure is the supervisor's; an error
// is a run the host could not start, and the queue tries the event again.
func (a *Agent) moduleTrigger(ctx context.Context, e event.Event) error {
	if e.Source != event.SourceModule {
		a.log.Warn("agent: a module trigger that is not the node's; dropped", "source", e.Source, "conv", e.Conv)
		return nil
	}
	var m event.ModuleTrigger
	if err := json.Unmarshal(e.Payload, &m); err != nil || m.Module == "" || m.Producer == "" || len(m.Cells) == 0 {
		a.log.Warn("agent: a module trigger the node cannot read; dropped", "event", e.ID, "conv", e.Conv)
		return nil
	}
	if err := a.d.ModuleTrigger(ctx, m, e.ID); err != nil {
		return fmt.Errorf("agent: module trigger of %s: %w", m.Module, err)
	}
	return nil
}
