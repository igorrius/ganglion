package modhost

import (
	"context"
	"slices"
	"strconv"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// reasonKeys are the /status reasons of a module that does not run.
var reasonKeys = map[Reason]texts.Key{
	ReasonLoad:      texts.StatusReasonModuleLoad,
	ReasonCells:     texts.StatusReasonModuleCells,
	ReasonInit:      texts.StatusReasonModuleInit,
	ReasonNoAdmin:   texts.StatusReasonModuleNoAdmin,
	ReasonCrashloop: texts.StatusReasonModuleCrashloop,
}

// Status implements status.Source (R-OBS-11): how many local modules run
// and are off, and a line for each that should run and does not, or that
// the node turned off; none when there are no local modules.
func (h *Host) Status(context.Context, status.Scope) ([]status.Check, error) {
	h.mu.RLock()
	running := len(h.running)
	h.mu.RUnlock()
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if running+len(h.off)+len(h.stopped) == 0 {
		return nil, nil
	}
	out := []status.Check{{Section: status.SectionNode, State: status.OK, Line: texts.StatusModulesLocal,
		Vars: texts.Vars{"running": strconv.Itoa(running), "off": strconv.Itoa(len(h.off))}}}
	names := make([]string, 0, len(h.stopped))
	for name := range h.stopped {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		st := h.stopped[name]
		line := texts.StatusModuleStopped
		if st.reason == ReasonCrashloop {
			line = texts.StatusModuleOff
		}
		out = append(out, status.Check{Section: status.SectionProblems, State: status.Warn, Line: line, Reason: reasonKeys[st.reason],
			Vars: texts.Vars{"module": name, "kind": string(st.kind)}, Since: st.since})
	}
	return out, nil
}
