package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// varFact is the texts' variable that holds a fact.
const varFact = "fact"

// ActionMemoryUndo is the Undo button of a "memory updated" note
// (R-MEM-9).
const ActionMemoryUndo = "memory.undo"

// memoryButtonsTTL is how long a memory note's buttons can be pressed.
const memoryButtonsTTL = 24 * time.Hour

// markUndo is the symbol on the Undo button, outside the translated label
// (R-LANG-6).
const markUndo = "↩️ "

// What an Undo press notes for the model, in English.
const (
	noteUndone       = "the owner pressed Undo on the memory note; reverted: %s"
	noteUndoConflict = "the owner pressed Undo on the memory note, but a later change touched the same facts; nothing was reverted"
)

// Undoer reverts memory commits (memory.Memory).
type Undoer interface {
	Undo(ctx context.Context, commits []string) error
}

// ButtonStore keeps quick-action buttons (pending.Store).
type ButtonStore interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// MemoryNotes makes the note that tells the owner what memory stored, with
// an Undo button, and answers that button (R-MEM-9).
type MemoryNotes struct {
	undo     Undoer
	buttons  ButtonStore
	now      func() time.Time
	location *time.Location
}

// NewMemoryNotes returns the notes over u, keeping their buttons in b; the
// time of an undo is written in loc.
func NewMemoryNotes(u Undoer, b ButtonStore, now func() time.Time, loc *time.Location) MemoryNotes {
	return MemoryNotes{undo: u, buttons: b, now: now, location: loc}
}

// undoArgs are an Undo button's arguments, fixed when the note was made:
// the commits to revert, the note's text to fold, and what changed, for
// the model's note.
type undoArgs struct {
	Commits []string `json:"commits"`
	Note    string   `json:"note"`
	What    []string `json:"what"`
}

// Note is the message that tells the owner what changed, in sp's
// language, with an Undo button bound to commits, for principal in conv.
// key is its IdemKey, so a rerun sends it once. ok is false when changes
// stored nothing or there is no commit to undo.
func (n MemoryNotes) Note(ctx context.Context, sp texts.Speaker, key, transport, conv, principal string,
	changes []memory.Change, commits []string,
) (event.Outgoing, bool, error) {
	var lines, what []string
	for _, c := range changes {
		if c.Held {
			continue
		}
		lines = append(lines, "- "+changeLine(sp, c))
		what = append(what, strings.TrimSpace(c.Slug+" "+c.Fact))
	}
	commits = nonEmpty(commits)
	if len(lines) == 0 || len(commits) == 0 {
		return event.Outgoing{}, false, nil
	}
	text := sp.Text(texts.MemoryUpdated, nil) + "\n" + strings.Join(lines, "\n")
	args, err := json.Marshal(undoArgs{Commits: commits, Note: text, What: what})
	if err != nil {
		return event.Outgoing{}, false, err
	}
	b := pending.Button{ID: pending.NewButtonID(), Action: ActionMemoryUndo, Label: markUndo + sp.Label(texts.ButtonUndo),
		Principal: principal, Conv: conv, Args: args, Expires: n.now().Add(memoryButtonsTTL)}
	if err := n.buttons.AddButtons(ctx, b); err != nil {
		return event.Outgoing{}, false, fmt.Errorf("memory: the note's button: %w", err)
	}
	return event.Outgoing{IdemKey: key, Transport: transport, Conv: conv, Text: text,
		Buttons: []event.Button{{ID: b.ID, Label: b.Label}}}, true, nil
}

// changeLine is one change in the owner's words; the stored text is
// escaped, so it shows as it was written.
func changeLine(sp texts.Speaker, c memory.Change) string {
	v := texts.Vars{"topic": texts.Escape(c.Title), varFact: texts.Escape(c.Text)}
	switch c.Kind {
	case memory.OpReplace:
		return sp.Text(texts.MemoryLineReplaced, v)
	case memory.OpRemove:
		return sp.Text(texts.MemoryLineRemoved, v)
	case memory.OpDrop:
		return sp.Text(texts.MemoryLineDropped, v)
	case memory.OpAdd:
	}
	return sp.Text(texts.MemoryLineAdded, v)
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// QuickActions are the memory notes' buttons by action name, for the
// agent (agent.Deps.QuickActions).
func (n MemoryNotes) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionMemoryUndo: n.undoByButton}
}

// undoByButton reverts the note's commits. The note folds under "Undone,
// 21:09"; when a later change touched the same facts nothing is reverted
// and the note says so. A second press of the same button (a rerun)
// changes nothing more: memory.Undo knows what it reverted.
func (n MemoryNotes) undoByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var a undoArgs
	if err := json.Unmarshal(p.Button.Args, &a); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("memory: button %s: arguments: %w", p.Button.ID, err)
	}
	err := n.undo.Undo(ctx, a.Commits)
	switch {
	case errors.Is(err, gitrepo.ErrConflict):
		return pending.QuickEdit{Text: a.Note + "\n\n" + p.Speak.Text(texts.MemoryUndoConflict, nil), Note: noteUndoConflict}, nil
	case err != nil:
		return pending.QuickEdit{}, err
	}
	line := p.Speak.Text(texts.LineUndone, nil) + ", " + n.now().In(n.location).Format("15:04")
	return pending.QuickEdit{Text: line, Folded: a.Note, Note: fmt.Sprintf(noteUndone, strings.Join(a.What, ", "))}, nil
}
