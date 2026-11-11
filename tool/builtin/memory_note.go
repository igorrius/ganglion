package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/gitrepo"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// The texts' variables that hold a fact and its topic.
const (
	varFact  = "fact"
	varTopic = "topic"
)

// The memory notes' buttons: Undo on a "memory updated" note (R-MEM-9);
// Remember and Skip on the facts a review found in a session with outside
// content (R-MEM-8, R-POL-6).
const (
	ActionMemoryUndo     = "memory.undo"
	ActionMemoryRemember = "memory.remember"
	ActionMemorySkip     = "memory.skip"
)

// memoryButtonsTTL is how long a memory note's buttons can be pressed.
const memoryButtonsTTL = 24 * time.Hour

// Symbols on the buttons, outside the translated label (R-LANG-6).
const (
	markUndo     = "↩️ "
	markRemember = "✅ "
	markSkip     = "✖️ "
)

// What a press notes for the model, in English.
const (
	noteUndone       = "the owner pressed Undo on the memory note; reverted: %s"
	noteUndoConflict = "the owner pressed Undo on the memory note, but a later change touched the same facts; nothing was reverted"
	noteRemembered   = "the owner pressed Remember on the memory review's facts; stored: %s"
	noteSkipped      = "the owner pressed Skip: the memory review's facts were not stored"
)

// MemoryStore is what the notes' buttons change and the question reads
// (memory.Memory).
type MemoryStore interface {
	Apply(ctx context.Context, ops []memory.Op, src memory.Source) (memory.Applied, error)
	Undo(ctx context.Context, commits []string) error
	Topic(slug string) (memory.Topic, error)
}

// ButtonStore keeps quick-action buttons (pending.Store).
type ButtonStore interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// MemoryNotes makes the note that tells the owner what memory stored, with
// an Undo button, and answers that button (R-MEM-9).
type MemoryNotes struct {
	mem      MemoryStore
	buttons  ButtonStore
	now      func() time.Time
	location *time.Location
}

// NewMemoryNotes returns the notes over m, keeping their buttons in b; the
// time of a press is written in loc.
func NewMemoryNotes(m MemoryStore, b ButtonStore, now func() time.Time, loc *time.Location) MemoryNotes {
	return MemoryNotes{mem: m, buttons: b, now: now, location: loc}
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
	v := texts.Vars{varTopic: texts.Escape(c.Title), varFact: texts.Escape(c.Text)}
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
	return map[string]pending.QuickAction{ActionMemoryUndo: n.undoByButton, ActionMemoryRemember: n.remember, ActionMemorySkip: n.skip}
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
	err := n.mem.Undo(ctx, a.Commits)
	switch {
	case errors.Is(err, gitrepo.ErrConflict):
		return pending.QuickEdit{Text: a.Note + "\n\n" + p.Speak.Text(texts.MemoryUndoConflict, nil), Note: noteUndoConflict}, nil
	case err != nil:
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{Text: n.stamped(p.Speak, texts.LineUndone), Folded: a.Note, Note: fmt.Sprintf(noteUndone, strings.Join(a.What, ", "))}, nil
}

// stamped is a closing line with the time of the press: "Undone, 21:09".
func (n MemoryNotes) stamped(sp texts.Speaker, k texts.Key) string {
	return sp.Text(k, nil) + ", " + n.now().In(n.location).Format("15:04")
}

// proposalArgs are the arguments of Remember and Skip: the facts and the
// question's text.
type proposalArgs struct {
	Ops  []memory.Op `json:"ops"`
	Note string      `json:"note"`
}

// Proposal is the question that shows the facts a review found in a
// session with outside content, whole, with Remember and Skip; nothing is
// stored before the owner's press (R-POL-6). key is its IdemKey.
func (n MemoryNotes) Proposal(ctx context.Context, sp texts.Speaker, key, transport, conv, principal string, ops []memory.Op) (event.Outgoing, error) {
	lines := make([]string, len(ops))
	for i, op := range ops {
		lines[i] = "- " + n.proposed(sp, op)
	}
	text := sp.Text(texts.MemoryProposal, texts.Vars{"facts": strings.Join(lines, "\n")})
	args, err := json.Marshal(proposalArgs{Ops: ops, Note: text})
	if err != nil {
		return event.Outgoing{}, err
	}
	expires := n.now().Add(memoryButtonsTTL)
	bs := []pending.Button{
		{ID: pending.NewButtonID(), Action: ActionMemoryRemember, Label: markRemember + sp.Label(texts.ButtonRemember), Principal: principal, Conv: conv, Args: args, Expires: expires},
		{ID: pending.NewButtonID(), Action: ActionMemorySkip, Label: markSkip + sp.Label(texts.ButtonSkip), Principal: principal, Conv: conv, Args: args, Expires: expires},
	}
	if err := n.buttons.AddButtons(ctx, bs...); err != nil {
		return event.Outgoing{}, fmt.Errorf("memory: the question's buttons: %w", err)
	}
	return event.Outgoing{IdemKey: key, Transport: transport, Conv: conv, Text: text,
		Buttons: []event.Button{{ID: bs[0].ID, Label: bs[0].Label}, {ID: bs[1].ID, Label: bs[1].Label}}}, nil
}

// remember stores the review's facts the owner approved, marked as from a
// session with outside content; each still passes the check. The question
// becomes the "memory updated" note with Undo. A fact that no longer fits
// memory is left out; one the check holds back is counted on the note.
// The keys are the button's, so a rerun of the press stores nothing twice.
func (n MemoryNotes) remember(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var a proposalArgs
	if err := json.Unmarshal(p.Button.Args, &a); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("memory: button %s: arguments: %w", p.Button.ID, err)
	}
	var changes []memory.Change
	var commits []string
	held := 0
	for i, op := range a.Ops {
		applied, err := n.mem.Apply(ctx, []memory.Op{op}, memory.Source{Turn: p.Event, Key: p.Button.ID + ":" + strconv.Itoa(i), Tainted: true})
		if errors.Is(err, memory.ErrInvalid) || errors.Is(err, memory.ErrNotFound) {
			continue
		}
		if err != nil {
			return pending.QuickEdit{}, err
		}
		for _, c := range applied.Changes {
			if c.Held {
				held++
			}
		}
		changes, commits = append(changes, applied.Changes...), append(commits, applied.Commit)
	}
	o, ok, err := n.Note(ctx, p.Speak, "", p.Transport, p.Button.Conv, p.Button.Principal, changes, commits)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	e := pending.QuickEdit{Text: n.stamped(p.Speak, texts.LineApproved), Folded: a.Note, Note: fmt.Sprintf(noteRemembered, "nothing")}
	if ok {
		e = pending.QuickEdit{Text: o.Text, Buttons: o.Buttons, Note: fmt.Sprintf(noteRemembered, whatOf(changes))}
	}
	if held > 0 {
		e.Text += "\n\n" + p.Speak.Text(texts.MemoryHeld, texts.Vars{"count": strconv.Itoa(held)})
	}
	return e, nil
}

// skip stores nothing and folds the question.
func (n MemoryNotes) skip(_ context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	var a proposalArgs
	if err := json.Unmarshal(p.Button.Args, &a); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("memory: button %s: arguments: %w", p.Button.ID, err)
	}
	return pending.QuickEdit{Text: n.stamped(p.Speak, texts.LineSkipped), Folded: a.Note, Note: noteSkipped}, nil
}

// whatOf names the stored changes: "inverter f1, car f2".
func whatOf(changes []memory.Change) string {
	var what []string
	for _, c := range changes {
		if !c.Held {
			what = append(what, strings.TrimSpace(c.Slug+" "+c.Fact))
		}
	}
	return strings.Join(what, ", ")
}

// proposed is one fact of the question, everything a model wrote escaped:
// a new topic with its summary, a correction with the text it replaces,
// a forget with the text it removes.
func (n MemoryNotes) proposed(sp texts.Speaker, op memory.Op) string {
	topic := op.Topic
	if op.Summary != "" {
		topic += " — " + op.Summary
	}
	v := texts.Vars{varTopic: texts.Escape(topic), varFact: texts.Escape(op.Text)}
	was := ""
	if t, err := n.mem.Topic(op.Topic); err == nil {
		for _, f := range t.Facts {
			if f.ID == op.Fact {
				was = f.Text
			}
		}
	}
	switch op.Kind {
	case memory.OpRemove:
		v[varFact] = texts.Escape(op.Fact + " " + was)
		return sp.Text(texts.MemoryLineForget, v)
	case memory.OpReplace:
		if was != "" {
			v[varFact] = texts.Escape(op.Text + " (was: " + was + ")")
		}
		return sp.Text(texts.MemoryLineReplaced, v)
	case memory.OpAdd, memory.OpDrop:
	}
	return sp.Text(texts.MemoryLineAdded, v)
}
