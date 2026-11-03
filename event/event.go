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

// Kinds.
const (
	// KindMessage is text a person sent through a chat transport; its
	// payload is a Message.
	KindMessage Kind = "message"
	// KindDecision is a press of a button the core sent; its payload is a
	// Decision.
	KindDecision Kind = "decision"
	// KindToolResult is the end of a pending tool call that nobody
	// answered in time; its payload is a ToolResult.
	KindToolResult Kind = "tool.result"
	// KindSchedule is a scheduled job's time: a reminder to send or a
	// task to run (spec 7.10); its payload is a Schedule.
	KindSchedule Kind = "schedule"
)

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
	// Buttons are shown under the message, one per row. A press comes back
	// as a KindDecision event carrying the button's ID.
	Buttons []Button `json:",omitempty"`
	// Edit is a Message.Ref of a message to change in place instead of
	// sending a new one; its buttons are replaced by Buttons (none removes
	// them). An Edit without Text changes only the buttons.
	Edit string `json:",omitempty"`
	// Ack is a Decision.Query: the message acknowledges that press with
	// Text as a short notice (a Telegram toast) and changes no message
	// (ruling R40).
	Ack string `json:",omitempty"`
}

// Button is one button under a message. ID is opaque to the transport and
// short enough for any channel's callback data; it never carries a tool
// name or arguments (ADR-0032).
type Button struct {
	ID    string
	Label string
}
