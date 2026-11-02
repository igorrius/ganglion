package event

// Message is the payload of an event of kind "message": text a person sent
// through a chat transport, in a form that does not depend on the channel.
type Message struct {
	Text string `json:"text"`
	From string `json:"from"`           // principal, e.g. "telegram:user:123"
	Name string `json:"name,omitempty"` // display name, for the model and logs
	Ref  string `json:"ref,omitempty"`  // the channel's reference to this message, opaque to the core
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
