// Package alert builds the messages that tell the node's admins about
// trouble: a daily limit reached, an event given up, polling stopped. Each
// admin gets the alert in their private chat on their transport.
package alert

import (
	"context"
	"fmt"

	"github.com/igorrius/ganglion/event"
)

// AdminsFunc lists the admin principals (access.List.Admins).
type AdminsFunc func(ctx context.Context) ([]string, error)

// PrivateConvFunc returns a principal's private conversation and its
// transport; ok is false when there is none (telegram.PrivateConv).
type PrivateConvFunc func(principal string) (transport, conv string, ok bool)

// Admins addresses alerts to every admin.
type Admins struct {
	List    AdminsFunc
	Private PrivateConvFunc
}

// Messages returns text addressed to every admin who has a private
// conversation. Each message's IdemKey is key + ":" + the admin, so an
// alert built again with the same key (a rerun turn, a repeat on the same
// day) is sent once.
func (a Admins) Messages(ctx context.Context, key, text string) ([]event.Outgoing, error) {
	admins, err := a.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("alert: %w", err)
	}
	var out []event.Outgoing
	for _, adm := range admins {
		if transport, conv, ok := a.Private(adm); ok {
			out = append(out, event.Outgoing{IdemKey: key + ":" + adm, Transport: transport, Conv: conv, Text: text})
		}
	}
	return out, nil
}
