package agent

import "github.com/igorrius/ganglion/event"

// dropLegacyTrigger acknowledges a module.trigger event and drops it. The
// node no longer puts one: cell triggers travel the bus (R-RUN-15,
// ADR-0108). One may still wait in the inbox from before the upgrade, and
// running it now would run a module on a change that is long past, so the
// event is ended with one info line. It is no turn: no model, no message,
// no conversation state. The kind goes with the release that follows the
// upgrade.
func (a *Agent) dropLegacyTrigger(e event.Event) {
	a.log.Info("agent: a module trigger left in the inbox from before the upgrade; dropped", "event", e.ID, "conv", e.Conv)
}
