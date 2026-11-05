package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/tool"
)

// Cells is the part of cells.Service the cells tools use.
type Cells interface {
	Get(ctx context.Context, id string) (cells.Cell, error)
	SetDesired(ctx context.Context, id string, v json.RawMessage, src string, expectVersion int64) (cells.Cell, error)
}

// Values a switch takes through cells.set.
const (
	valueOn  = "on"
	valueOff = "off"
)

// cellView is what the model reads about a cell.
type cellView struct {
	ID       string          `json:"id"`
	Kind     cells.Kind      `json:"kind"`
	Desired  json.RawMessage `json:"desired"`
	Reported json.RawMessage `json:"reported"`
	InSync   bool            `json:"in_sync"`
}

// viewOf is c as compact JSON text.
func viewOf(c cells.Cell) (string, error) {
	b, err := json.Marshal(cellView{ID: c.ID, Kind: c.Kind, Desired: c.Desired, Reported: c.Reported, InSync: c.InSync()})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cellID reads the "id" argument of a call.
func cellID(name string, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("%s: arguments: %w", name, err)
	}
	if a.ID == "" {
		return "", fmt.Errorf("%s: id is empty", name)
	}
	return a.ID, nil
}

// cellError names the cell in the error of a missing one.
func cellError(name, id string, err error) error {
	if errors.Is(err, cells.ErrNotFound) {
		return fmt.Errorf("%s: unknown cell %q: %w", name, id, err)
	}
	return fmt.Errorf("%s: %s: %w", name, id, err)
}

// CellsGet is the cells.get tool.
type CellsGet struct{ cells Cells }

// NewCellsGet returns the cells.get tool over c.
func NewCellsGet(c Cells) *CellsGet { return &CellsGet{cells: c} }

// Spec describes cells.get.
func (*CellsGet) Spec() tool.Spec {
	return tool.Spec{
		Name:        "cells.get",
		Description: "Read a state cell, such as a switch or a sensor: its desired and reported values and whether they agree (in_sync).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","description":"the cell id, such as load.boiler"}},` +
			`"required":["id"],"additionalProperties":false}`),
		Class:     tool.ClassRead,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
		LocalSafe: true,
	}
}

// Run returns the cell's id, kind, desired, reported and in_sync as
// compact JSON text; an unknown cell is an error.
func (g *CellsGet) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	id, err := cellID("cells.get", c.Args)
	if err != nil {
		return tool.Result{}, err
	}
	cell, err := g.cells.Get(ctx, id)
	if err != nil {
		return tool.Result{}, cellError("cells.get", id, err)
	}
	text, err := viewOf(cell)
	if err != nil {
		return tool.Result{}, fmt.Errorf("cells.get: %w", err)
	}
	return tool.Result{Text: text}, nil
}

// CellsSet is the cells.set tool.
type CellsSet struct{ cells Cells }

// NewCellsSet returns the cells.set tool over c.
func NewCellsSet(c Cells) *CellsSet { return &CellsSet{cells: c} }

// Spec describes cells.set. The policy confirms every call: it is a side
// effect on the home.
func (*CellsSet) Spec() tool.Spec {
	return tool.Spec{
		Name:        "cells.set",
		Description: "Switch a cell on or off by setting its desired value. The owner confirms the change before it is made.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","description":"the id of a switch cell, such as load.boiler"},` +
			`"value":{"type":"string","enum":["on","off"],"description":"the desired state"}},` +
			`"required":["id","value"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

// setArgs are the arguments of cells.set.
type setArgs struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// args reads and checks a call's arguments: a cell id and "on" or "off".
func (*CellsSet) args(c tool.Call) (setArgs, error) {
	var a setArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return setArgs{}, fmt.Errorf("cells.set: arguments: %w", err)
	}
	if a.ID == "" {
		return setArgs{}, errors.New("cells.set: id is empty")
	}
	if a.Value != valueOn && a.Value != valueOff {
		return setArgs{}, fmt.Errorf(`cells.set: value must be "on" or "off", got %q`, a.Value)
	}
	return a, nil
}

// Question implements tool.Questioner: the cell and the value it is set
// to, both whole (ruling R34).
func (s *CellsSet) Question(_ context.Context, c tool.Call) (string, error) {
	a, err := s.args(c)
	if err != nil {
		return "", err
	}
	return whole("cells.set", "Switch "+a.ID+" "+a.Value+"?")
}

// Run sets the cell's desired value to "on" or "off", with the call's
// key as the source of the change, and returns the cell as cells.get
// does. Any other value is refused.
func (s *CellsSet) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	a, err := s.args(c)
	if err != nil {
		return tool.Result{}, err
	}
	cell, err := s.cells.SetDesired(ctx, a.ID, json.RawMessage(`"`+a.Value+`"`), c.Key(), 0)
	if err != nil {
		return tool.Result{}, cellError("cells.set", a.ID, err)
	}
	text, err := viewOf(cell)
	if err != nil {
		return tool.Result{}, fmt.Errorf("cells.set: %w", err)
	}
	return tool.Result{Text: text}, nil
}
