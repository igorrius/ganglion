package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
)

// errNotOwn is a report of a cell that is not the module's own.
var errNotOwn = errors.New("modhost: not a cell the module owns")

// cellPort is the runner's CellPort for one module over the node's cells
// (ADR-0062). The runner lets a module report only the cells its manifest
// declares, under its prefix; the port checks again that the cell is under
// that prefix and owned by the module, so an ownerless cell (the module was
// removed) or another module's is never written.
type cellPort struct {
	cells  *cells.Service
	module string // the owner, "local/demo-reader"
	prefix string // the short name, "demo-reader"
}

// Report implements runner.CellPort.
func (p cellPort) Report(ctx context.Context, id string, v json.RawMessage, _ string) error {
	if !strings.HasPrefix(id, p.prefix+".") {
		return errNotOwn
	}
	c, err := p.cells.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Owner != p.module {
		return errNotOwn
	}
	if _, err = p.cells.Report(ctx, id, v, cells.CauseExternal, "module:"+p.module); err != nil {
		return err
	}
	if r, ok := runOf(ctx); ok { // the run's listeners start when it ends (R-RUN-15)
		r.wrote.add(id)
	}
	return nil
}

// Get implements runner.CellPort: the cell's reported value and when it
// last changed.
func (p cellPort) Get(ctx context.Context, id string) (json.RawMessage, time.Time, error) {
	c, err := p.cells.Get(ctx, id)
	if err != nil {
		return nil, time.Time{}, err
	}
	return c.Reported, c.Updated, nil
}
