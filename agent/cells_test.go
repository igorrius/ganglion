package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// cellsEnv is an agent over a cell service with a sensor of a module and a
// switch, at 10:00 on t0's day.
func cellsEnv(t *testing.T) (*env, *cells.Service) {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	svc := cells.NewService(cells.NewMemStore(), cells.WithClock(func() time.Time { return e.now }))
	ctx := context.Background()
	if err := svc.DefineOwned(ctx, cells.OwnedDef{ID: "deye-reader.soc", Owner: "local/deye-reader", Kind: cells.KindSensor, About: "Battery *charge*, %"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Define(ctx, cells.Def{ID: "load.boiler", Kind: cells.KindSwitch}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DefineOwned(ctx, cells.OwnedDef{ID: "deye-reader.mode", Owner: "local/deye-reader", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	report := func(id, v string, ago time.Duration) {
		e.now = t0.Add(-ago)
		if _, err := svc.Report(ctx, id, json.RawMessage(v), cells.CauseExternal, "module:deye-reader"); err != nil {
			t.Fatal(err)
		}
	}
	report("deye-reader.soc", "70", 40*time.Minute)
	report("deye-reader.soc", "78", 12*time.Minute)
	e.now = t0
	e.deps.Cells, e.deps.Buttons = svc, e.pending
	e.a = New(overDefaults(Config{}), e.deps)
	return e, svc
}

// /cells is a golden list: a message a cell with its value, its age and its
// about (here and nowhere else), and a History button; no model.
func TestCellsCommandGolden(t *testing.T) {
	e, _ := cellsEnv(t)
	out := e.turn(t, message(owner, "Igor", "/cells"))
	want := []string{
		"deye-reader.mode  no value  just now",
		"deye-reader.soc  78  12 min ago\nBattery \\*charge\\*, %",
		"load.boiler  no value  just now",
	}
	var got []string
	for _, o := range out {
		got = append(got, o.Text)
		if len(o.Buttons) != 1 || o.Buttons[0].Label != "History" {
			t.Errorf("buttons %+v", o.Buttons)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cells:\n%q\nwant:\n%q", got, want)
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
	for _, m := range e.state(t).Messages {
		if strings.Contains(m.Content, "charge") || strings.Contains(m.Content, "deye-reader") {
			t.Fatalf("the history holds the module's words: %q", m.Content)
		}
	}
}

// The History button shows the last changes of its cell in its own message,
// with no model.
func TestCellsHistoryButton(t *testing.T) {
	e, _ := cellsEnv(t)
	out := e.turn(t, message(owner, "Igor", "/cells"))
	var soc event.Outgoing
	for _, o := range out {
		if strings.HasPrefix(o.Text, "deye-reader.soc") {
			soc = o
		}
	}
	e.smart.push()
	res := e.turn(t, press(owner, soc.Buttons[0].ID, "55"))
	var edit event.Outgoing
	for _, o := range res {
		if o.Edit == "55" && strings.Contains(o.Text, "the last changes") {
			edit = o
		}
	}
	if edit.Text == "" {
		t.Fatalf("no history edit in %+v", res)
	}
	want := "deye-reader.soc  78  12 min ago\nBattery \\*charge\\*, %\n\ndeye-reader.soc: the last changes\n" +
		"03.11 09:48  78\n03.11 09:20  70"
	if edit.Text != want {
		t.Fatalf("edit:\n%s\nwant:\n%s", edit.Text, want)
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
}

func TestCellsCommandNoneAndAdminOnly(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	if got := oneReply(t, e, "/cells"); got != "No cells." {
		t.Fatalf("none: %q", got)
	}
	e.admin = false
	if got := oneReply(t, e, "/cells"); got != replyTasksNotAvailable {
		t.Fatalf("not admin: %q", got)
	}
}

// failingCells is a cell service whose Get breaks; only a missing cell is "gone".
type failingCells struct {
	NoCells
	err error
}

func (f failingCells) Get(context.Context, string) (cells.Cell, error) { return cells.Cell{}, f.err }

func TestCellsHistoryButtonOnlyMissingCellsAreGone(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.Buttons = e.pending
	args, _ := json.Marshal(cellRef{ID: "x.y"})
	press := pending.QuickPress{Speak: texts.Speaker{}, Button: pending.Button{ID: "b", Args: args}}
	for _, c := range []struct {
		err  error
		gone bool
	}{{cells.ErrNotFound, true}, {errors.New("db down"), false}} {
		e.deps.Cells = failingCells{err: c.err}
		a := New(overDefaults(Config{}), e.deps)
		edit, err := a.historyButton(context.Background(), press)
		if c.gone && (err != nil || edit.Text != "This cell no longer exists.") {
			t.Errorf("missing: %+v, %v", edit, err)
		}
		if !c.gone && (err == nil || edit.Text != "") {
			t.Errorf("broken store: %+v, %v", edit, err)
		}
	}
}
