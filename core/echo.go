package core

import (
	"context"
	"encoding/json"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue"
)

// Echo replies "echo: <text>" to a message, through the transport it came
// from. Other events, and messages whose payload cannot be read, get no
// reply. It is the handler of M2; the agent replaces it in M3.
func Echo(e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
	if e.Kind != "message" {
		return nil, queue.StateUpdate{}, nil
	}
	var m event.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil || m.Text == "" {
		// Retrying would not fix the payload: complete without a reply.
		return nil, queue.StateUpdate{}, nil
	}
	return []event.Outgoing{{
		IdemKey:   e.IdemKey + ":reply:0",
		Transport: e.Source,
		Conv:      e.Conv,
		Text:      "echo: " + m.Text,
	}}, queue.StateUpdate{}, nil
}

// EchoHandler is Echo as a Handler.
func EchoHandler(_ context.Context, e event.Event) ([]event.Outgoing, queue.StateUpdate, error) {
	return Echo(e)
}
