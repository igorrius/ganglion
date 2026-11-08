package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// SwitchCell is the kill switch of host.exec (R-POL-5): "off" stops new
// commands; commands already running finish. An admin sets it with
// /commands; cells.set refuses it.
const SwitchCell = "tools.host_exec"

// readWait bounds a read of the switch when no context is at hand.
const readWait = time.Second

// Switch is host.exec's kill switch.
type Switch struct{ cells *cells.Service }

// NewSwitch defines the switch cell and returns it.
func NewSwitch(ctx context.Context, cs *cells.Service) (*Switch, error) {
	if err := cs.Define(ctx, cells.Def{ID: SwitchCell, Kind: cells.KindSwitch}); err != nil {
		return nil, err
	}
	return &Switch{cells: cs}, nil
}

// On reports whether commands may run: the cell was never set, or is "on".
// A cell that cannot be read is off.
func (s *Switch) On(ctx context.Context) bool {
	c, err := s.cells.Get(ctx, SwitchCell)
	if err != nil {
		return false
	}
	if len(c.Desired) == 0 {
		return true
	}
	var v string
	return json.Unmarshal(c.Desired, &v) == nil && v == "on"
}

// Open is On with its own short context, for a tool's Offered.
func (s *Switch) Open() bool {
	ctx, cancel := context.WithTimeout(context.Background(), readWait)
	defer cancel()
	return s.On(ctx)
}

// Set turns commands on or off for the principal by.
func (s *Switch) Set(ctx context.Context, on bool, by string) error {
	v := `"off"`
	if on {
		v = `"on"`
	}
	_, err := s.cells.SetDesired(ctx, SwitchCell, json.RawMessage(v), by, 0)
	return err
}

// Name implements status.Source.
func (*Switch) Name() string { return "commands" }

// Status implements status.Source: a line only while commands are off.
func (s *Switch) Status(ctx context.Context, _ status.Scope) ([]status.Check, error) {
	if s.On(ctx) {
		return nil, nil
	}
	return []status.Check{{Section: status.SectionProblems, State: status.Off, Line: texts.StatusCommandsOff}}, nil
}
