package event

// Message is the payload of an event of kind "message": text a person sent
// through a chat transport, in a form that does not depend on the channel.
type Message struct {
	Text string `json:"text"`
	From string `json:"from"`           // principal, e.g. "telegram:user:123"
	Name string `json:"name,omitempty"` // display name, for the model and logs
	Ref  string `json:"ref,omitempty"`  // the channel's reference to this message, opaque to the core
}
