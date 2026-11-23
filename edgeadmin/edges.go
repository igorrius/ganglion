package edgeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// revocation is what a Revoke button carries.
type revocation struct {
	Name string `json:"name"`
}

// Card is /edges (R-EDGE-5): each joined edge with the time it joined in
// loc and a Revoke button, stored for principal in conv. The permissions
// are kept as a digest and are not listed.
func (a *Admin) Card(ctx context.Context, sp texts.Speaker, principal, conv string, loc *time.Location) (string, []event.Button, error) {
	edges, err := a.d.Service.Edges(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("edgeadmin: /edges: %w", err)
	}
	if len(edges) == 0 {
		return sp.Text(texts.EdgeListNone, nil), nil, nil
	}
	lines := []string{sp.Text(texts.EdgeListHead, nil)}
	stored := make([]pending.Button, 0, len(edges))
	for _, e := range edges {
		lines = append(lines, "- "+sp.Text(texts.EdgeListRow, texts.Vars{varName: text.CodeSpan(e.Name), varTime: e.Joined.In(loc).Format("2 Jan 15:04")}))
		b, err := a.revokeButton(sp, principal, conv, e.Name)
		if err != nil {
			return "", nil, err
		}
		stored = append(stored, b)
	}
	lines = append(lines, "", sp.Text(texts.EdgeListNote, nil))
	if err := a.d.Buttons.AddButtons(ctx, stored...); err != nil {
		return "", nil, fmt.Errorf("edgeadmin: /edges: %w", err)
	}
	buttons := make([]event.Button, len(stored))
	for i, b := range stored {
		buttons[i] = event.Button{ID: b.ID, Label: b.Label}
	}
	return strings.Join(lines, "\n"), buttons, nil
}

// revokeButton makes (without storing) a Revoke button of the edge.
func (a *Admin) revokeButton(sp texts.Speaker, principal, conv, name string) (pending.Button, error) {
	args, err := json.Marshal(revocation{Name: name})
	if err != nil {
		return pending.Button{}, fmt.Errorf("edgeadmin: the revoke button: %w", err)
	}
	return pending.Button{ID: pending.NewButtonID(), Action: ActionRevoke,
		Label:     markRevoke + sp.LabelWith(texts.ButtonEdgeRevoke, texts.Vars{varName: name}),
		Principal: principal, Conv: conv, Args: args, Expires: a.now().Add(buttonsTTL)}, nil
}

// revokeByButton revokes the edge: its key is refused, its connection
// dropped, its events no longer sourced, and it leaves the list. An edge
// that is gone already says so. Revoke is idempotent, so a failure of the
// bus leaves the card with a new Revoke button to press again.
func (a *Admin) revokeByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	if err := a.admin(ctx, p.Button.Principal); err != nil {
		return pending.QuickEdit{}, err
	}
	var r revocation
	if err := json.Unmarshal(p.Button.Args, &r); err != nil || r.Name == "" {
		return pending.QuickEdit{}, fmt.Errorf("edgeadmin: button arguments")
	}
	vars := texts.Vars{varName: text.CodeSpan(r.Name)}
	err := a.d.Service.Revoke(ctx, r.Name)
	switch {
	case err == nil:
		return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeRevoked, vars),
			Note: "the owner revoked the edge " + r.Name + " by button with /edges; its key is refused and its connection is dropped"}, nil
	case errors.Is(err, edgejoin.ErrUnknownEdge):
		return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeGone, vars),
			Note: "the owner pressed Revoke for the edge " + r.Name + ", which was gone already"}, nil
	}
	a.log.Warn("edgeadmin: an edge was not revoked", "edge", r.Name, "err", err)
	b, berr := a.revokeButton(p.Speak, p.Button.Principal, p.Button.Conv, r.Name)
	if berr == nil {
		berr = a.d.Buttons.AddButtons(ctx, b)
	}
	if berr != nil {
		return pending.QuickEdit{}, fmt.Errorf("edgeadmin: revoke %s: %w", r.Name, err)
	}
	return pending.QuickEdit{Text: p.Speak.Text(texts.EdgeRevokeFailed, vars), Buttons: []event.Button{{ID: b.ID, Label: b.Label}},
		Note: "the owner pressed Revoke for the edge " + r.Name + ", but the node could not finish it; the button stays to press again"}, nil
}
