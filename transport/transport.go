// Package transport defines how events enter a node and replies leave it:
// Telegram and MQTT first, HTTP and Home Assistant later (docs/spec.md,
// section 5.3).
package transport

import (
	"context"

	"github.com/igorrius/ganglion/event"
)

// Sink receives events from a transport. A transport must not advance its
// own offset (for example the Telegram update offset) until Sink has
// returned nil, or an error wrapping ErrDropped: the event is then durably
// in the inbox, or deliberately not kept (R-DLV-2).
type Sink func(context.Context, event.Event) error

// Transport is a channel to people or devices.
type Transport interface {
	// Name identifies the transport, e.g. "telegram".
	Name() string
	// Run receives events and hands them to sink until ctx is cancelled.
	Run(ctx context.Context, sink Sink) error
	// Send delivers one outgoing message. The outbox sender retries it, so
	// Send should honour msg.IdemKey where the channel allows it.
	Send(ctx context.Context, msg event.Outgoing) error
}

// Lang is a language a chat client may be in, as an IETF code; it picks
// the text a transport shows that client. Languages are a closed set.
type Lang string

// Languages. LangDefault is for a client in any language not listed.
const (
	LangDefault   Lang = ""
	LangRussian   Lang = "ru"
	LangUkrainian Lang = "uk"
)

// Command is a chat command a transport lists for people, such as
// Telegram's command hints (ruling R43): its name without the slash and
// its description per language. LangDefault must be described; a language
// a command does not describe shows the default (ruling R45).
type Command struct {
	Name        string
	Description map[Lang]string
}
