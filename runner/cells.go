package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/pdk"
)

// CellPort is the runner's side of the cells: the node's adapter over its
// cell service (modhost). The runner never imports the cells package.
type CellPort interface {
	// Report sets a sensor cell the module owns. src names who reports.
	Report(ctx context.Context, id string, v json.RawMessage, src string) error
	// Get returns a cell's value and when it last changed.
	Get(ctx context.Context, id string) (json.RawMessage, time.Time, error)
}

// noCells is the port of a module that has no cells: nothing is declared,
// so nothing reaches it.
type noCells struct{}

func (noCells) Report(context.Context, string, json.RawMessage, string) error {
	return errors.New("runner: no cell port")
}

func (noCells) Get(context.Context, string) (json.RawMessage, time.Time, error) {
	return nil, time.Time{}, errors.New("runner: no cell port")
}

// MemCells is an in-memory CellPort for the development tools (modrun,
// modbench) and tests: it keeps every reported value, with the time it
// was reported.
type MemCells struct {
	now    func() time.Time
	mu     sync.Mutex
	values map[string]memCell
}

type memCell struct {
	value json.RawMessage
	at    time.Time
}

// NewMemCells returns an empty port whose reports are timed by now.
func NewMemCells(now func() time.Time) *MemCells {
	return &MemCells{now: now, values: map[string]memCell{}}
}

// Report implements CellPort.
func (c *MemCells) Report(_ context.Context, id string, v json.RawMessage, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[id] = memCell{value: slices.Clone(v), at: c.now()}
	return nil
}

// Get implements CellPort.
func (c *MemCells) Get(_ context.Context, id string) (json.RawMessage, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.values[id]
	if !ok {
		return nil, time.Time{}, fmt.Errorf("runner: no cell %s", id)
	}
	return v.value, v.at, nil
}

// Values returns every cell's value by id.
func (c *MemCells) Values() map[string]json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]json.RawMessage, len(c.values))
	for id, v := range c.values {
		out[id] = v.value
	}
	return out
}

// Results of the cell_report host function.
const (
	cellOK     int32 = 0
	cellDenied int32 = -1 // not declared by the manifest, or not the module's own
	cellType   int32 = -2 // the value does not fit the cell's type
)

// cellReport is the cell_report host function: the cell must be declared
// under cells.report, which is also what makes it the module's own. The
// check comes before the port, so a name the manifest does not list (
// another module's cell, a path) never reaches it.
func (m *Module) cellReport(ctx context.Context, name string, value []byte) int32 {
	m.called(ctx, CallCellReport)
	decl, ok := m.manifest.Cells.Report[name]
	if !ok {
		m.deps.Logger.Warn("cell_report denied", "module", m.manifest.Name)
		return cellDenied
	}
	if err := decl.Type.Check(value); err != nil {
		// The module's call fails with the node's own words, whatever the
		// module answers (R-RUN-14).
		m.formatErr = fmt.Sprintf("cell %s: %v", name, err)
		return cellType
	}
	err := m.deps.Cells.Report(ctx, m.cellID(name), value, m.deps.Prefix)
	if err != nil {
		m.deps.Logger.Warn("cell_report failed", "module", m.manifest.Name, "cell", name, "err", err)
		return cellDenied
	}
	return cellOK
}

// cellGet is the cell_get host function: a cell the module owns by its
// short name, or a full id listed under cells.read.
func (m *Module) cellGet(ctx context.Context, name string) int32 {
	m.called(ctx, CallCellGet)
	id := name
	if _, own := m.manifest.Cells.Report[name]; own {
		id = m.cellID(name)
	} else if !slices.Contains(m.manifest.Cells.Read, name) {
		return -1
	}
	v, at, err := m.deps.Cells.Get(ctx, id)
	if err != nil {
		return -1
	}
	b, err := marshal(struct {
		Value json.RawMessage `json:"value"`
		AtMs  int64           `json:"at_ms"`
	}{v, at.UnixMilli()})
	if err != nil {
		return -1
	}
	return m.offer(string(b), true)
}

func (m *Module) cellID(name string) string {
	return m.deps.Prefix + "." + name
}

// Init runs the module's init trigger, at load. A module that does not
// know the trigger is fine; a failure is an error with the module's own
// text, for the host to log and show the owner.
func (m *Module) Init(ctx context.Context) error {
	return m.lifecycle(ctx, "init")
}

// Health runs the module's health trigger, at install and on the card.
func (m *Module) Health(ctx context.Context) error {
	return m.lifecycle(ctx, "health")
}

func (m *Module) lifecycle(ctx context.Context, trigger string) error {
	out, err := m.Handle(ctx, pdk.Input{Trigger: trigger})
	if err != nil {
		return err
	}
	if out.OK || out.Error == pdk.ErrUnknownTrigger {
		return nil
	}
	m.deps.Logger.Warn("module "+trigger+" failed", "module", m.manifest.Name, "kind", out.Kind, "error", out.Error)
	return lifecycleError(m.manifest.Name, trigger, out)
}

// lifecycleError words a failed init or health in the node's own text
// (R-RUN-14): the module's text goes to the log only. KindFormat's text is
// the node's already.
func lifecycleError(module, trigger string, out pdk.Output) error {
	switch out.Kind {
	case pdk.KindFormat:
		return &ModuleError{Module: module, Kind: out.Kind, Msg: out.Error}
	case pdk.KindAuthFailed:
		return &ModuleError{Module: module, Kind: out.Kind, Msg: trigger + " failed: credentials rejected"}
	}
	return &ModuleError{Module: module, Kind: pdk.KindException, Msg: trigger + " failed"}
}

// checkAnswer is the contract of a tool answer: the output its trigger
// declared, if any. The result is module text, so a mismatch is worded
// from the shape alone (R-RUN-14).
func (m *Module) checkAnswer(trigger string, out pdk.Output) pdk.Output {
	if !out.OK {
		return out
	}
	shape := m.answerShape(trigger)
	if shape == nil {
		return out
	}
	if err := shape.Check(out.Result); err != nil {
		return pdk.Output{Kind: pdk.KindFormat, Error: err.Error()}
	}
	return out
}

// answerShape finds the output declared for a tool trigger: "tool:<name>"
// names the tool, plain "tool" (a tool module, one tool) takes the first.
func (m *Module) answerShape(trigger string) *Shape {
	name, ok := toolName(trigger)
	if !ok {
		return nil
	}
	for _, t := range m.manifest.Triggers {
		if t.Tool != "" && (name == "" || t.Tool == name) {
			return t.Output
		}
	}
	return nil
}

func toolName(trigger string) (string, bool) {
	if trigger == "tool" {
		return "", true
	}
	return strings.CutPrefix(trigger, "tool:")
}
