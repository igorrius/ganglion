package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

// varGroupID is the placeholder of a group's key in the card.
const varGroupID = "id"

// groupRequest asks an admin, in their private chat, whether a group chat
// that is not connected may talk to the bot, and tells that group once
// that it has asked, with no model (R-DLV-17, ADR-0076). Only the node's
// event counts, and only for an admin. The card is the node's text: the
// group's title and the sender's name are outside text and go in escaped.
// The notice is in the admin's language: the node does not know the
// group's people yet.
func (a *Agent) groupRequest(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	p := t.groupReq
	if t.event.Source != event.SourceGroups {
		a.log.Warn("agent: a group request that is not the node's; dropped", "source", t.event.Source, "conv", t.event.Conv)
		return nil
	}
	t.transport, t.record.Principal = p.Transport, p.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: a group request for a principal who is not an admin; dropped", "group", p.Group, "principal", p.Principal)
		return nil
	}
	buttons, err := a.groupButtons(ctx, t, p)
	if err != nil {
		return err
	}
	vars := texts.Vars{varTitle: texts.Escape(p.Title), varGroupID: p.Group, varName: texts.Escape(p.Name)}
	t.out = append(t.out,
		event.Outgoing{IdemKey: t.event.IdemKey + ":card", Transport: p.Transport, Conv: t.event.Conv,
			Text: a.say(ctx, t, texts.GroupRequestCard, vars), Buttons: buttons, NoPreview: true},
		event.Outgoing{IdemKey: t.event.IdemKey + ":notice", Transport: p.Transport, Conv: p.Conv,
			Text: a.say(ctx, t, texts.GroupNotConnected, nil)})
	return nil
}

// groupButtons stores the Allow and Refuse buttons of p for the admin, in
// the conversation of the card, and returns them. A turn that runs again
// stores two more; its messages are dropped by their keys, so those are
// never shown and run out.
func (a *Agent) groupButtons(ctx context.Context, t *turn, p event.GroupRequest) ([]event.Button, error) {
	args, err := json.Marshal(groups.ButtonArgs{Group: p.Group, Title: p.Title})
	if err != nil {
		return nil, fmt.Errorf("agent: group request: %w", err)
	}
	expires := a.d.Now().Add(groups.RequestTTL)
	stored := []pending.Button{
		{ID: pending.NewButtonID(), Action: groups.ActionAllow, Label: a.label(ctx, t, texts.ButtonAllow),
			Principal: p.Principal, Conv: t.event.Conv, Args: args, Expires: expires},
		{ID: pending.NewButtonID(), Action: groups.ActionRefuse, Label: a.label(ctx, t, texts.ButtonRefuse),
			Principal: p.Principal, Conv: t.event.Conv, Args: args, Expires: expires},
	}
	if err := a.d.Pending.AddButtons(ctx, stored...); err != nil {
		return nil, fmt.Errorf("agent: group request: %w", err)
	}
	out := make([]event.Button, len(stored))
	for i, b := range stored {
		out[i] = event.Button{ID: b.ID, Label: b.Label}
	}
	return out, nil
}
