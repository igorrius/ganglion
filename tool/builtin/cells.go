package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// Cells is the part of cells.Service the cells tools use.
type Cells interface {
	Get(ctx context.Context, id string) (cells.Cell, error)
	List(ctx context.Context) ([]cells.Cell, error)
	// HistorySince is the changes of a cell at or after since, oldest first.
	HistorySince(ctx context.Context, id string, since time.Time, limit int) ([]cells.Change, error)
	SetDesired(ctx context.Context, id string, v json.RawMessage, src string, expectVersion int64) (cells.Cell, error)
}

// errToolSwitch is the refusal of a tools.* cell: the owner's switches.
var errToolSwitch = errors.New("tools.* cells are the owner's switches; they set them with /commands")

// toolSwitchPrefix starts the id of a cell that only an admin's command sets.
const toolSwitchPrefix = "tools."

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
	History  *historyView    `json:"history,omitempty"`
}

// viewOf is c as compact JSON text.
func viewOf(c cells.Cell) (string, error) {
	return viewWith(c, nil)
}

func viewWith(c cells.Cell, h *historyView) (string, error) {
	b, err := json.Marshal(cellView{ID: c.ID, Kind: c.Kind, Desired: c.Desired, Reported: c.Reported, InSync: c.InSync(), History: h})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// outside reports whether a value of c is text from outside the node's own
// words: a module wrote it, or a sensor reported it. Any string in it taints
// the session (R-RUN-13); the "on" and "off" of a switch the node controls
// do not.
func outside(c cells.Cell, values ...json.RawMessage) bool {
	if c.Owner == "" && c.Kind == cells.KindSwitch {
		return false
	}
	for _, v := range values {
		if len(v) > 0 && runner.HasString(v) {
			return true
		}
	}
	return false
}

// cellError names the cell in the error of a missing one.
func cellError(name, id string, err error) error {
	if errors.Is(err, cells.ErrNotFound) {
		return fmt.Errorf("%s: unknown cell %q: %w", name, id, err)
	}
	return fmt.Errorf("%s: %s: %w", name, id, err)
}

// CellsList is the cells.list tool.
type CellsList struct {
	cells Cells
	now   func() time.Time
}

// NewCellsList returns the cells.list tool over c.
func NewCellsList(c Cells, now func() time.Time) *CellsList { return &CellsList{cells: c, now: now} }

// Spec describes cells.list.
func (*CellsList) Spec() tool.Spec {
	return tool.Spec{
		Name: "cells.list",
		Description: "List the state cells, such as sensors and switches: id, kind, owner (the module that reports it), " +
			"current value and how many seconds ago it last changed. Read one cell's history with cells.get.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Class:       tool.ClassRead,
		Policy:      tool.PolicyAuto,
		AdminOnly:   true,
		LocalSafe:   true,
	}
}

// listedCell is one cell in the answer of cells.list. A cell's `about` is for the
// owner, not the model (R-RUN-13).
type listedCell struct {
	ID    string          `json:"id"`
	Kind  cells.Kind      `json:"kind"`
	Owner string          `json:"owner"`
	Value json.RawMessage `json:"value"`
	AgeS  int64           `json:"age_s"`
}

// valueOf is what a cell holds now: the reported value, else the desired.
func valueOf(c cells.Cell) json.RawMessage {
	v := c.Reported
	if len(v) == 0 {
		v = c.Desired
	}
	if len(v) == 0 {
		return json.RawMessage(`null`)
	}
	return v
}

// Run lists the cells, requests excepted, as a JSON array. The answer is
// outside content when a cell of a module or a sensor holds a string.
func (t *CellsList) Run(ctx context.Context, _ tool.Call) (tool.Result, error) {
	all, err := t.cells.List(ctx)
	if err != nil {
		return tool.Result{}, fmt.Errorf("cells.list: %w", err)
	}
	out := make([]listedCell, 0, len(all))
	external := false
	for _, c := range all {
		if c.Kind == cells.KindRequest {
			continue
		}
		v := valueOf(c)
		external = external || outside(c, v)
		out = append(out, listedCell{ID: c.ID, Kind: c.Kind, Owner: c.Owner, Value: v, AgeS: max(0, int64(t.now().Sub(c.Updated).Seconds()))})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return tool.Result{}, fmt.Errorf("cells.list: %w", err)
	}
	return tool.Result{Text: string(b), External: external}, nil
}

// CellsGet is the cells.get tool.
type CellsGet struct {
	cells Cells
	now   func() time.Time
}

// NewCellsGet returns the cells.get tool over c.
func NewCellsGet(c Cells, now func() time.Time) *CellsGet { return &CellsGet{cells: c, now: now} }

// Spec describes cells.get.
func (*CellsGet) Spec() tool.Spec {
	return tool.Spec{
		Name: "cells.get",
		Description: "Read a state cell, such as a switch or a sensor: its desired and reported values and whether they agree (in_sync). " +
			"With since (a duration such as 6h, at most 168h) it also gives the history of the reported value, condensed to at most 48 points, " +
			"with min, max and mean when every value is a number.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","description":"the cell id, such as load.boiler"},` +
			`"since":{"type":"string","description":"how far back to read the history, a Go duration such as 90m or 24h, at most 168h"}},` +
			`"required":["id"],"additionalProperties":false}`),
		Class:     tool.ClassRead,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
		LocalSafe: true,
	}
}

// getArgs are the arguments of cells.get.
type getArgs struct {
	ID    string `json:"id"`
	Since string `json:"since"`
}

// maxSince is the furthest back a history is read.
const maxSince = 7 * 24 * time.Hour

// sinceOf parses the "since" argument; 0 when it is absent.
func sinceOf(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > maxSince {
		return 0, fmt.Errorf("cells.get: since must be a duration such as 90m or 24h, at most 168h")
	}
	return d, nil
}

// Run returns the cell's id, kind, desired, reported and in_sync as compact
// JSON text, and with since the condensed history; an unknown cell is an
// error. The text is outside content when a cell of a module or a sensor
// holds a string.
func (g *CellsGet) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a getArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("cells.get: arguments: %w", err)
	}
	if a.ID == "" {
		return tool.Result{}, errors.New("cells.get: id is empty")
	}
	since, err := sinceOf(a.Since)
	if err != nil {
		return tool.Result{}, err
	}
	cell, err := g.cells.Get(ctx, a.ID)
	if err != nil {
		return tool.Result{}, cellError("cells.get", a.ID, err)
	}
	values := []json.RawMessage{cell.Desired, cell.Reported}
	var h *historyView
	if since > 0 {
		now := g.now()
		changes, err := g.cells.HistorySince(ctx, a.ID, now.Add(-since), maxChanges)
		if err != nil {
			return tool.Result{}, cellError("cells.get", a.ID, err)
		}
		h = condense(changes, now, since)
		for _, p := range h.Points {
			values = append(values, p.Value)
		}
	}
	text, err := viewWith(cell, h)
	if err != nil {
		return tool.Result{}, fmt.Errorf("cells.get: %w", err)
	}
	return tool.Result{Text: text, External: outside(cell, values...)}, nil
}

// History limits.
const (
	// maxChanges is the most changes read from the store for one history.
	maxChanges = 10000
	// maxPoints is the most points a condensed history holds.
	maxPoints = 48
)

// point is one reported value, as many seconds before now.
type point struct {
	AgeS  int64           `json:"age_s"`
	Value json.RawMessage `json:"v"`
}

// historyView is a cell's reported values over a time, condensed.
type historyView struct {
	SinceS    int64    `json:"since_s"`
	Changes   int      `json:"changes"` // how many reported changes the time held
	Truncated bool     `json:"truncated,omitempty"`
	Points    []point  `json:"points"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Mean      *float64 `json:"mean,omitempty"`
}

// condense keeps the reported changes, evenly picked down to maxPoints with
// the first and the last always in, oldest first. When every value is a
// number, min, max and mean cover all the changes, not only the points
// kept; any other value leaves them out.
func condense(changes []cells.Change, now time.Time, since time.Duration) *historyView {
	var reported []cells.Change
	for _, ch := range changes {
		if ch.Field == cells.FieldReported {
			reported = append(reported, ch)
		}
	}
	h := &historyView{SinceS: int64(since.Seconds()), Changes: len(reported), Truncated: len(changes) >= maxChanges, Points: []point{}}
	for _, i := range picks(len(reported), maxPoints) {
		h.Points = append(h.Points, point{AgeS: max(0, int64(now.Sub(reported[i].At).Seconds())), Value: reported[i].Value})
	}
	h.Min, h.Max, h.Mean = stats(reported)
	return h
}

// picks is up to k indexes of n, evenly spread, the first and the last
// included.
func picks(n, k int) []int {
	if n <= k {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, k)
	for i := range out {
		out[i] = i * (n - 1) / (k - 1)
	}
	return out
}

// stats is the minimum, maximum and mean of the values when all are numbers;
// nil when there are none or any is not a number.
func stats(changes []cells.Change) (lo, hi, mean *float64) {
	if len(changes) == 0 {
		return nil, nil, nil
	}
	l, h, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, ch := range changes {
		var f float64
		if json.Unmarshal(ch.Value, &f) != nil {
			return nil, nil, nil
		}
		l, h, sum = min(l, f), max(h, f), sum+f
	}
	m := sum / float64(len(changes))
	return &l, &h, &m
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
	if strings.HasPrefix(a.ID, toolSwitchPrefix) {
		return setArgs{}, fmt.Errorf("cells.set: %w", errToolSwitch)
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
	return whole("cells.set", c.Speak.Text(texts.AskSwitchCell, texts.Vars{"id": a.ID, "value": a.Value}))
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
