// Package event defines the single input and output shapes that every
// transport speaks (docs/spec.md, section 6).
package event

import (
	"encoding/json"
	"time"
)

// Kind is what an event carries. Kinds are stored in the inbox, so their
// values never change.
type Kind string

// KindMessage is text a person sent through a chat transport; its payload
// is a Message.
const KindMessage Kind = "message"

// Event is the single input from any transport: a chat message, a sensor
// reading, a cron tick or a module exception.
type Event struct {
	ID      string // ULID
	IdemKey string // Telegram: update_id; cron: job + scheduled time
	Source  string // "telegram", "mqtt", "cron", "module:<name>"
	Conv    string // conversation / actor key
	Kind    Kind   // KindMessage; later "sensor", "module.exception", ...
	Payload json.RawMessage
	TTL     time.Duration // 0 means the event never expires
	At      time.Time     // when the event was produced
}

// Expired reports whether the event's TTL has run out at now. An event
// without a TTL never expires. A command such as "open the gate" that
// arrives after its deadline must be dropped, not executed (docs/spec.md, 9.1).
func (e Event) Expired(now time.Time) bool {
	return e.TTL > 0 && now.Sub(e.At) > e.TTL
}

// Outgoing is a message the core sends back through a transport.
type Outgoing struct {
	ID        string // ULID
	IdemKey   string // derived from turn_id + call number (R-DLV-6)
	Transport string // transport name, e.g. "telegram"
	Conv      string // conversation the message belongs to
	Text      string
	Silent    bool   // deliver without a notification sound
	ReplyTo   string // a Message.Ref this answers; the transport decides whether to show it as a reply
}
