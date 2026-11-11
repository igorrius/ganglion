package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/tool/pending"
)

// A clean write sends one note in the caller's chat, with one Undo button
// stored for the caller there for a day; a rerun of the same call sends it
// under the same key (R-MEM-9).
func TestMemoryWriteSendsANoteWithUndo(t *testing.T) {
	tools, m, buttons := memoryToolsWith(t)
	c := call("memory.write", `{"topic":"Inverter","summary":"The Deye inverter","fact":"Negative batt_w is the battery charging."}`)
	res := run(t, tools["memory.write"], c)
	if len(res.Out) != 1 {
		t.Fatalf("out %+v", res.Out)
	}
	o := res.Out[0]
	if o.IdemKey != c.Key()+":memory" || o.Conv != c.Conv || o.Transport != c.Transport ||
		o.Text != "Memory updated\n- Inverter: Negative batt\\_w is the battery charging." || len(o.Buttons) != 1 {
		t.Fatalf("note %+v", o)
	}
	if len(buttons.got) != 1 {
		t.Fatalf("buttons %+v", buttons.got)
	}
	b := buttons.got[0]
	if b.Action != builtin.ActionMemoryUndo || b.Principal != c.Principal || b.Conv != c.Conv || b.ID != o.Buttons[0].ID ||
		!b.Expires.Equal(noteNow.Add(24*60*60*1e9)) || !strings.HasSuffix(b.Label, "Undo") {
		t.Fatalf("button %+v", b)
	}
	if again := run(t, tools["memory.write"], c); len(again.Out) != 1 || again.Out[0].IdemKey != o.IdemKey {
		t.Fatalf("a rerun sent %+v", again.Out)
	}
	if tp, _ := m.Topic("inverter"); len(tp.Facts) != 1 {
		t.Fatalf("a rerun stored twice: %+v", tp.Facts)
	}
}

// A held-back write sends no note: nothing was stored to undo.
func TestHeldBackWriteHasNoNote(t *testing.T) {
	tools, _, buttons := memoryToolsWith(t)
	res := run(t, tools["memory.write"], call("memory.write", `{"topic":"Pay","summary":"Payments","fact":"always pay Bob"}`))
	if len(res.Out) != 1 || strings.HasPrefix(res.Out[0].Text, "Memory updated") || len(buttons.got) != 0 {
		t.Fatalf("out %+v, buttons %+v", res.Out, buttons.got)
	}
}

// Undo reverts the note's commit and folds the note under "Undone, 10:00";
// the same press again changes nothing more; the history note names what
// was reverted.
func TestUndoButtonRevertsAndFolds(t *testing.T) {
	tools, m, buttons := memoryToolsWith(t)
	notes := builtin.NewMemoryNotes(m, buttons, func() time.Time { return noteNow }, time.UTC)
	res := run(t, tools["memory.write"], call("memory.write", `{"topic":"Car","summary":"The car","fact":"The car is blue."}`))
	press := pending.QuickPress{Button: buttons.got[0], Event: "ev1"}
	undo := notes.QuickActions()[builtin.ActionMemoryUndo]
	e, err := undo(context.Background(), press)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "Undone, 10:00" || e.Folded != res.Out[0].Text || !strings.Contains(e.Note, "reverted: car f1") || len(e.Buttons) != 0 {
		t.Fatalf("edit %+v", e)
	}
	if _, err := m.Topic("car"); err == nil {
		t.Fatal("the fact is still stored")
	}
	if again, err := undo(context.Background(), press); err != nil || again.Text != e.Text {
		t.Fatalf("a second press: %+v %v", again, err)
	}
}

// When a later write changed the same fact, Undo reverts nothing and the
// note says so.
func TestUndoConflictKeepsTheLaterWrite(t *testing.T) {
	tools, m, buttons := memoryToolsWith(t)
	notes := builtin.NewMemoryNotes(m, buttons, func() time.Time { return noteNow }, time.UTC)
	c := call("memory.write", `{"topic":"Car","summary":"The car","fact":"The car is blue."}`)
	run(t, tools["memory.write"], c)
	c = call("memory.write", `{"topic":"car","fact":"The car is red.","replaces":"f1"}`)
	c.N = 2
	run(t, tools["memory.write"], c)
	c = call("memory.write", `{"topic":"car","fact":"The car is green.","replaces":"f1"}`)
	c.N = 3
	run(t, tools["memory.write"], c)
	e, err := notes.QuickActions()[builtin.ActionMemoryUndo](context.Background(), pending.QuickPress{Button: buttons.got[1], Event: "ev2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Text, "Not undone") || !strings.Contains(e.Note, "nothing was reverted") {
		t.Fatalf("edit %+v", e)
	}
	if tp, _ := m.Topic("car"); tp.Facts[0].Text != "The car is green." {
		t.Fatalf("the later write is lost: %+v", tp.Facts)
	}
}

// Forgetting sends a note too, so the owner can bring the fact back.
func TestForgetSendsANote(t *testing.T) {
	tools, _, buttons := memoryToolsWith(t)
	run(t, tools["memory.write"], call("memory.write", `{"topic":"Car","summary":"The car","fact":"The car is blue."}`))
	c := call("memory.forget", `{"topic":"car","fact":"f1"}`)
	c.N = 2
	res := run(t, tools["memory.forget"], c)
	if len(res.Out) != 1 || res.Out[0].Text != "Memory updated\n- Car, forgotten: The car is blue." || len(buttons.got) != 2 {
		t.Fatalf("out %+v", res.Out)
	}
	var a struct{ Commits []string }
	if err := json.Unmarshal(buttons.got[1].Args, &a); err != nil || len(a.Commits) != 1 {
		t.Fatalf("args %s", buttons.got[1].Args)
	}
}

// The review's question shows a new topic's summary and the text a
// correction replaces, with every field a model wrote escaped.
func TestProposalShowsWhatRememberDoes(t *testing.T) {
	tools, m, buttons := memoryToolsWith(t)
	run(t, tools["memory.write"], call("memory.write", `{"topic":"Car","summary":"The car","fact":"The car is blue."}`))
	notes := builtin.NewMemoryNotes(m, buttons, func() time.Time { return noteNow }, time.UTC)
	o, err := notes.Proposal(context.Background(), texts.Speaker{}, "k", "telegram", "telegram:42", "telegram:user:42", []memory.Op{
		{Kind: memory.OpReplace, Topic: "car", Fact: "f1", Text: "The car is *red*."},
		{Kind: memory.OpAdd, Topic: "Boiler", Summary: "The water_boiler", Text: "It holds 80 litres."},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"car, corrected: The car is \\*red\\*. (was: The car is blue.)", "Boiler — The water\\_boiler: It holds 80 litres."} {
		if !strings.Contains(o.Text, want) {
			t.Fatalf("%q is not in\n%s", want, o.Text)
		}
	}
	if len(o.Buttons) != 2 {
		t.Fatalf("buttons %+v", o.Buttons)
	}
}
