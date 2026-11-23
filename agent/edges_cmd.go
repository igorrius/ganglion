package agent

import (
	"context"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport"
)

// EdgesAdmin is what /edges shows (edgeadmin.Admin.Card): each joined edge
// with the time it joined and a Revoke button, stored for principal in conv.
type EdgesAdmin interface {
	Card(ctx context.Context, sp texts.Speaker, principal, conv string, loc *time.Location) (string, []event.Button, error)
}

// NoEdges is a node that takes no edges.
type NoEdges struct{}

// Card says no edge is connected.
func (NoEdges) Card(_ context.Context, sp texts.Speaker, _, _ string, _ *time.Location) (string, []event.Button, error) {
	return sp.Text(texts.EdgeListNone, nil), nil, nil
}

// edgesHint is /edges' description in the command hints.
func edgesHint() map[transport.Lang]string {
	return map[transport.Lang]string{
		transport.LangDefault:   "Connected edges (your computers): the list, and Revoke",
		transport.LangRussian:   "Подключённые edge (ваши компьютеры): список и отзыв",
		transport.LangUkrainian: "Підключені edge (ваші комп'ютери): список і відкликання"}
}

// edgesCmd is /edges (R-EDGE-5): the joined edges, for an admin in their
// private chat, without a model and with nothing in the history. Its Revoke
// buttons are quick actions, whose press notes the change.
func (a *Agent) edgesCmd(ctx context.Context, t *turn, _ string) error {
	return a.cardCmd(ctx, t, "/edges", texts.EdgePrivateOnly, a.d.Edges.Card)
}
