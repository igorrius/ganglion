package natsbus

import (
	"context"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// Name implements status.Source.
func (b *Bus) Name() string { return "bus" }

// Status implements status.Source (R-OBS-11): the mode and the store in use
// while the node is connected, a failing line when it is not, and a
// failing line while the bus runs without its trust chain because the
// secret store is locked.
func (b *Bus) Status(ctx context.Context, _ status.Scope) ([]status.Check, error) {
	h := b.Health(ctx)
	vars := texts.Vars{"mode": string(h.Mode)}
	if !h.Connected {
		return []status.Check{{Section: status.SectionProblems, State: status.Fail, Line: texts.StatusBusDown, Vars: vars}}, nil
	}
	vars["size"] = status.Bytes(int64(h.StoreBytes))
	checks := []status.Check{{Section: status.SectionProblems, State: status.OK, Line: texts.StatusBusOK, Vars: vars}}
	if b.cfg.ChainLocked {
		checks = append(checks, status.Check{Section: status.SectionProblems, State: status.Fail, Line: texts.StatusBusNoChain, Vars: texts.Vars{"mode": string(h.Mode)}})
	}
	return checks, nil
}

var _ status.Source = (*Bus)(nil)
