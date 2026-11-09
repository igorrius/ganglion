package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// CellsView is what /cells reads (cells.Service).
type CellsView interface {
	List(ctx context.Context) ([]cells.Cell, error)
	Get(ctx context.Context, id string) (cells.Cell, error)
	// History is the newest changes of a cell first.
	History(ctx context.Context, id string, limit int) ([]cells.Change, error)
}

// NoCells is a node with no cells to show.
type NoCells struct{}

// List lists nothing.
func (NoCells) List(context.Context) ([]cells.Cell, error) { return nil, nil }

// Get has no cell.
func (NoCells) Get(context.Context, string) (cells.Cell, error) {
	return cells.Cell{}, cells.ErrNotFound
}

// History has no cell.
func (NoCells) History(context.Context, string, int) ([]cells.Change, error) { return nil, nil }

// Buttons stores quick-action buttons (pending.Store); the buttons of /cells
// are not offered without it.
type Buttons interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// NoButtons stores nothing: a node without it shows /cells without buttons.
type NoButtons struct{}

// AddButtons stores nothing.
func (NoButtons) AddButtons(context.Context, ...pending.Button) error { return nil }

const (
	// ActionCellHistory is the quick action of a cell's History button.
	ActionCellHistory = "cells.history"
	// cellsShown is how many cells /cells shows, a message each.
	cellsShown = 20
	// historyShown is how many changes the History button shows.
	historyShown = 20
	// cellsButtonsTTL is how long the buttons of /cells work.
	cellsButtonsTTL = 24 * time.Hour
	// maxShownValue is the longest value of a cell /cells shows, in runes.
	maxShownValue = 80
	cellsNote     = commandPrefix + "the owner listed the cells with /cells"
)

// cellRef is the argument of a History button.
type cellRef struct {
	ID string `json:"id"`
}

// listCells is /cells: a message for each cell with its value and age, its
// `about` (shown here and nowhere else, R-RUN-13) and a History button; no
// model. Requests are not cells to read. The history gets a note that the
// list was shown, not what it held: a module's text never reaches the model.
func (a *Agent) listCells(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	all, err := a.d.Cells.List(ctx)
	if err != nil {
		return fmt.Errorf("agent: /cells: %w", err)
	}
	var shown []cells.Cell
	for _, c := range all {
		if c.Kind != cells.KindRequest {
			shown = append(shown, c)
		}
	}
	if len(shown) == 0 {
		t.reply(a.say(ctx, t, texts.CellsNone, nil))
		return nil
	}
	sp := a.speaker(ctx, t)
	for i, c := range shown[:min(len(shown), cellsShown)] {
		buttons, err := a.historyButtons(ctx, sp, t, c.ID)
		if err != nil {
			return fmt.Errorf("agent: /cells: %w", err)
		}
		t.out = append(t.out, event.Outgoing{IdemKey: t.event.IdemKey + ":cells:" + strconv.Itoa(i), Transport: t.transport, Conv: t.replyConv,
			Text: a.cellRow(sp, c, true), Buttons: buttons})
	}
	if len(shown) > cellsShown {
		t.reply(a.say(ctx, t, texts.CellsMore, texts.Vars{"count": strconv.Itoa(len(shown) - cellsShown)}))
	}
	t.note(cellsNote)
	return nil
}

// historyButtons is the History button of a cell, stored for the person
// who asked; none when the node stores no buttons.
func (a *Agent) historyButtons(ctx context.Context, sp texts.Speaker, t *turn, id string) ([]event.Button, error) {
	if a.d.Buttons == (NoButtons{}) {
		return nil, nil
	}
	args, err := json.Marshal(cellRef{ID: id})
	if err != nil {
		return nil, err
	}
	b := pending.Button{ID: pending.NewButtonID(), Action: ActionCellHistory, Label: sp.Label(texts.ButtonHistory),
		Principal: t.msg.From, Conv: t.event.Conv, Args: args, Expires: a.d.Now().Add(cellsButtonsTTL)}
	if err := a.d.Buttons.AddButtons(ctx, b); err != nil {
		return nil, err
	}
	return []event.Button{{ID: b.ID, Label: b.Label}}, nil
}

// cellRow is `deye-reader.soc  78  12 min ago`, and under it the cell's
// about when withAbout.
func (a *Agent) cellRow(sp texts.Speaker, c cells.Cell, withAbout bool) string {
	about := ""
	if withAbout && c.About != "" {
		about = "\n" + texts.Escape(c.About)
	}
	return sp.Text(texts.CellsRow, texts.Vars{"id": texts.Escape(c.ID), varValue: a.cellValue(sp, c),
		"age": a.age(sp, a.d.Now().Sub(c.Updated)), "about": about})
}

// cellValue is what the cell holds now: its reported value, else the desired
// one, on one line.
func (a *Agent) cellValue(sp texts.Speaker, c cells.Cell) string {
	v := c.Reported
	if v == nil {
		v = c.Desired
	}
	if v == nil {
		return sp.Text(texts.CellsNoValue, nil)
	}
	return texts.Escape(shownValue(v))
}

// shownValue is a JSON value as a person reads it: a string without its
// quotes, anything else as written, cut short.
func shownValue(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) != nil {
		s = string(v)
	}
	return text.Truncate(strings.Join(strings.Fields(s), " "), maxShownValue)
}

// age is how long ago, in the largest of minutes, hours and days.
func (*Agent) age(sp texts.Speaker, d time.Duration) string {
	switch {
	case d < time.Minute:
		return sp.Text(texts.CellsAgeNow, nil)
	case d < time.Hour:
		return sp.Text(texts.CellsAgeMin, texts.Vars{"n": strconv.Itoa(int(d / time.Minute))})
	case d < 24*time.Hour:
		return sp.Text(texts.CellsAgeHour, texts.Vars{"n": strconv.Itoa(int(d / time.Hour))})
	}
	return sp.Text(texts.CellsAgeDay, texts.Vars{"n": strconv.Itoa(int(d / (24 * time.Hour)))})
}

// historyButton shows a cell's last changes in its own message, with no
// model; a cell that is gone says so.
func (a *Agent) historyButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var ref cellRef
	if err := json.Unmarshal(p.Button.Args, &ref); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("agent: button %s: arguments: %w", p.Button.ID, err)
	}
	c, err := a.d.Cells.Get(ctx, ref.ID)
	if err != nil {
		return pending.QuickEdit{Text: p.Speak.Text(texts.CellsGone, nil)}, nil
	}
	changes, err := a.d.Cells.History(ctx, ref.ID, historyShown)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	changes = slices.DeleteFunc(changes, func(ch cells.Change) bool { return ch.Field == cells.FieldCreated })
	id := texts.Escape(c.ID)
	head := a.cellRow(p.Speak, c, true)
	if len(changes) == 0 {
		return pending.QuickEdit{Text: head + "\n\n" + p.Speak.Text(texts.CellsHistoryNone, texts.Vars{"id": id})}, nil
	}
	rows := make([]string, len(changes))
	for i, ch := range changes {
		value := shownValue(ch.Value)
		if ch.Field != cells.FieldReported {
			value = ch.Field + " " + value
		}
		rows[i] = p.Speak.Text(texts.CellsHistoryRow, texts.Vars{"at": ch.At.In(a.cfg.Location).Format("02.01 15:04"), varValue: texts.Escape(value)})
	}
	return pending.QuickEdit{Text: head + "\n\n" + p.Speak.Text(texts.CellsHistoryHead, texts.Vars{"id": id}) + "\n" + strings.Join(rows, "\n")}, nil
}
