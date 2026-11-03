package event

// Message is the payload of an event of kind "message": text a person sent
// through a chat transport, in a form that does not depend on the channel.
type Message struct {
	Text string `json:"text"`
	From string `json:"from"`           // principal, e.g. "telegram:user:123"
	Name string `json:"name,omitempty"` // display name, for the model and logs
	Ref  string `json:"ref,omitempty"`  // the channel's reference to this message, opaque to the core
	// Forwarded marks Text as someone else's words: a forward, or a post
	// the channel forwarded on its own. Quote is text of another message
	// the sender quoted; ExternalReply marks a reply to a message in
	// another chat. All three are outside content: the agent wraps the
	// text as data and taints the session (ruling R35). The sender's own
	// words in a message with a quote or an external reply stay plain.
	Forwarded     bool   `json:"forwarded,omitempty"`
	Quote         string `json:"quote,omitempty"`
	ExternalReply bool   `json:"external_reply,omitempty"`
}

// Decision is the payload of an event of kind "decision": a person pressed
// a button. From uses the key "from", as Message does, so the access list
// drops a stranger's press the same way it drops a stranger's message.
type Decision struct {
	Button string `json:"button"`         // the Button.ID
	From   string `json:"from"`           // principal who pressed
	Name   string `json:"name,omitempty"` // display name
	Ref    string `json:"ref,omitempty"`  // the message holding the button, for an Edit
}

// ToolResult is the payload of an event of kind "tool.result": a pending
// tool call ended without a press.
type ToolResult struct {
	Pending string `json:"pending"` // the pending call's id
}

// Schedule is the payload of an event of kind "schedule": the time of a
// scheduled job came, or its owner asked to run it now. The job itself
// is read from its store when the event is handled, so a job deleted
// after the event was stored does not run.
type Schedule struct {
	Job       string `json:"job"`          // the job's id
	Scheduled int64  `json:"scheduled_ms"` // the slot, Unix ms; for a run now, when it was asked
}
