package edgeadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// ToolJoin is the tool that proposes connecting a computer as an edge.
const ToolJoin = "edge.join"

// Actions of the cards' buttons.
const (
	ActionApprove = "edge.card.approve"
	ActionCancel  = "edge.card.cancel"
	ActionRevoke  = "edge.revoke"
)

// Symbols on the buttons, outside the translated label (R-LANG-6).
const (
	markApprove = "✅ "
	markCancel  = "✖️ "
	markRevoke  = "🔌 "
)

// joinSchema is edge.join's arguments.
var joinSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"name":{"type":"string","description":"A short name for the computer, one lowercase word of a-z, 0-9, - or _, such as home."}},` +
	`"required":["name"],"additionalProperties":false}`)

// Errors of edge.join the model reads: the node's own words.
var (
	errArgs    = errors.New("arguments must be name")
	errBadName = errors.New("the name must be one word of lowercase letters, digits, - or _, and not core")
	errTaken   = errors.New("an edge by that name is connected already: choose another name, " +
		"or the owner revokes it with /edges first")
	errGrant   = errors.New("a scheduled run does not connect edges")
	errPrivate = errors.New("edges are connected only in the admin's private chat")
	errFailed  = errors.New("the node could not prepare the card now; try again later")
	errNoEdges = errors.New("this node cannot take edges: its bus has no trust chain, or the node has no public listener " +
		"for an edge to reach; the owner sets that up in the node's file")
)

// joinTool is edge.join.
type joinTool struct{ a *Admin }

// Spec implements tool.Handler. The card is the approval: the tool asks
// nothing first, and nothing is made until the owner presses Approve.
func (joinTool) Spec() tool.Spec {
	return tool.Spec{Name: ToolJoin, InputSchema: joinSchema, Class: tool.ClassSideEffect, Policy: tool.PolicyAuto, AdminOnly: true,
		Description: "Connect the owner's computer to this node as an edge, under a name. " +
			"The owner gets a card with what the computer would be allowed to do; nothing is made until they press Approve, " +
			"and then the card shows them a one-time code to type on the computer. You never see the code."}
}

// Run implements tool.Handler.
func (t joinTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	name, err := nameOf(c)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, err)
	}
	if c.Grant != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errGrant)
	}
	if err := t.a.admin(ctx, c.Principal); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, err)
	}
	if _, conv, ok := t.a.d.Private(c.Principal); !ok || conv != c.Conv || c.Transport == "" {
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errPrivate)
	}
	prop, err := t.a.d.Service.Propose(ctx, name)
	switch {
	case errors.Is(err, edgejoin.ErrNameTaken):
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errTaken)
	case errors.Is(err, ErrUnavailable):
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errNoEdges)
	case errors.Is(err, bus.ErrBadToken):
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errBadName)
	case err != nil:
		t.a.log.Warn("edgeadmin: a card was not made", "edge", name, "err", err)
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errFailed)
	}
	out, err := t.a.card(ctx, c, prop)
	if err != nil {
		t.a.log.Warn("edgeadmin: a card was not made", "edge", name, "err", err)
		return tool.Result{}, fmt.Errorf("%s: %w", ToolJoin, errFailed)
	}
	return tool.Result{Text: "The owner got a card for the edge " + name + " with what it would be allowed to do. " +
		"Nothing is connected until they press Approve; the code appears only in the card, never to you, " +
		"and you get a note when they press.", Out: []event.Outgoing{out}}, nil
}

// nameOf reads and checks the call's arguments.
func nameOf(c tool.Call) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return "", errArgs
	}
	if bus.CheckEdgeName(args.Name) != nil {
		return "", errBadName
	}
	return args.Name, nil
}

// pressed is what the Approve and Cancel buttons carry: the name and the
// digest of the permissions the card showed, fixed when the card was made.
type pressed struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// card makes the card of the proposal with its Approve and Cancel buttons.
func (a *Admin) card(ctx context.Context, c tool.Call, p edgejoin.Proposal) (event.Outgoing, error) {
	sp := c.Speak
	body := sp.Text(texts.EdgeCard, texts.Vars{varName: text.CodeSpan(p.Name), varPublish: a.subjects(sp, p.Permissions.Publish),
		varSubscribe: a.subjects(sp, p.Permissions.Subscribe)})
	args, err := json.Marshal(pressed{Name: p.Name, Digest: p.Digest})
	if err != nil {
		return event.Outgoing{}, fmt.Errorf("edgeadmin: the card's buttons: %w", err)
	}
	expires := a.now().Add(buttonsTTL)
	stored := []pending.Button{
		{ID: pending.NewButtonID(), Action: ActionApprove, Label: markApprove + sp.Label(texts.ButtonEdgeApprove)},
		{ID: pending.NewButtonID(), Action: ActionCancel, Label: markCancel + sp.Label(texts.ButtonEdgeCancel)},
	}
	buttons := make([]event.Button, len(stored))
	for i := range stored {
		stored[i].Principal, stored[i].Conv, stored[i].Args, stored[i].Expires = c.Principal, c.Conv, args, expires
		buttons[i] = event.Button{ID: stored[i].ID, Label: stored[i].Label}
	}
	if err := a.d.Buttons.AddButtons(ctx, stored...); err != nil {
		return event.Outgoing{}, fmt.Errorf("edgeadmin: the card's buttons: %w", err)
	}
	return event.Outgoing{IdemKey: c.Key() + ":edge.card", Transport: c.Transport, Conv: c.Conv, Text: body, Buttons: buttons}, nil
}

// subjects is a list of subjects as lines of data: each in a code span.
func (a *Admin) subjects(sp texts.Speaker, list []string) string {
	if len(list) == 0 {
		return sp.Text(texts.EdgeNone, nil)
	}
	lines := make([]string, len(list))
	for i, s := range list {
		lines[i] = "- " + text.CodeSpan(s)
	}
	return strings.Join(lines, "\n")
}

// read reads a button's arguments after checking who pressed.
func (a *Admin) read(ctx context.Context, p pending.QuickPress) (pressed, error) {
	if err := a.admin(ctx, p.Button.Principal); err != nil {
		return pressed{}, err
	}
	var ap pressed
	if err := json.Unmarshal(p.Button.Args, &ap); err != nil {
		return pressed{}, fmt.Errorf("edgeadmin: button arguments: %w", err)
	}
	if err := bus.CheckEdgeName(ap.Name); err != nil {
		return pressed{}, fmt.Errorf("edgeadmin: button arguments: a bad edge name")
	}
	return ap, nil
}

// approveByButton makes the one-time code for the card the owner read and
// shows it in the card, once. A press on a card whose permissions are not
// those the node would give now makes no code. A rerun of the press makes
// a new code, which replaces the earlier one.
func (a *Admin) approveByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	ap, err := a.read(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	code, exp, err := a.d.Service.Approve(ctx, ap.Name, ap.Digest)
	vars := texts.Vars{varName: text.CodeSpan(ap.Name)}
	switch {
	case errors.Is(err, edgejoin.ErrStale):
		return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeStale, vars),
			Note: "the owner pressed Approve for the edge " + ap.Name + ", but its permissions had changed: no code was made"}, nil
	case errors.Is(err, edgejoin.ErrNameTaken):
		return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeTaken, vars),
			Note: "the owner pressed Approve for the edge " + ap.Name + ", but an edge by that name is connected: no code was made"}, nil
	case errors.Is(err, edgejoin.ErrTooMany):
		return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeBusy, nil),
			Note: "the owner pressed Approve for the edge " + ap.Name + ", but too many edges or codes are open: no code was made"}, nil
	case err != nil:
		return pending.QuickEdit{}, fmt.Errorf("edgeadmin: approve %s: %w", ap.Name, err)
	}
	vars[varTime] = a.timeIn(exp)
	vars[varCommand] = text.CodeSpan("ganglion edge join " + a.address(p.Speak) + " " + code)
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.EdgeApproved, vars),
		// The note holds no code: the model reads it.
		Note: "the owner approved connecting the edge " + ap.Name + " by button: a one-time code was shown to them in the card, " +
			"valid for ten minutes; it is not available to you",
	}, nil
}

// address is the node's address for the command the card shows.
func (a *Admin) address(sp texts.Speaker) string {
	if u := a.d.Address(); u != "" {
		return u
	}
	return sp.Text(texts.EdgeAddressUnknown, nil)
}

// cancelByButton closes the card: nothing was made, so there is nothing to
// undo.
func (a *Admin) cancelByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	ap, err := a.read(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.EdgeCancelled, texts.Vars{varName: text.CodeSpan(ap.Name)}),
		Note: "the owner pressed Cancel: the edge " + ap.Name + " was not connected",
	}, nil
}
