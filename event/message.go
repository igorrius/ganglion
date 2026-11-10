package event

import "time"

// Message is the payload of an event of kind "message": text a person sent
// through a chat transport, in a form that does not depend on the channel.
type Message struct {
	Text string `json:"text"`
	From string `json:"from"`           // principal, e.g. "telegram:user:123"
	Name string `json:"name,omitempty"` // display name, for the model and logs
	Ref  string `json:"ref,omitempty"`  // the channel's reference to this message, opaque to the core
	// Lang is the language the channel reports for the sender, raw and
	// unchecked; language.Store.Seen checks it (R-LANG-1).
	Lang string `json:"lang,omitempty"`
	// Forwarded marks Text as someone else's words: a forward, or a post
	// the channel forwarded on its own. Quote is text of another message
	// the sender quoted; ExternalReply marks a reply to a message in
	// another chat. All three are outside content: the agent wraps the
	// text as data and taints the session (ruling R35). The sender's own
	// words in a message with a quote or an external reply stay plain.
	Forwarded     bool   `json:"forwarded,omitempty"`
	Quote         string `json:"quote,omitempty"`
	ExternalReply bool   `json:"external_reply,omitempty"`
	// Files are the files that came with the message. Nothing downloads
	// them yet: the agent is told that they came (R-DLV-10).
	Files []File `json:"files,omitempty"`
	// Reply is the message this one answers, as the channel reports it
	// (R-DLV-13); nil when it answers none.
	Reply *Replied `json:"reply,omitempty"`
}

// Replied is a message a person answered with a reply, as the channel reports it
// (R-DLV-13). Its text is the channel's copy, outside content unless the node's own
// outbox holds the message (R-DLV-14).
type Replied struct {
	Ref       string    `json:"ref"`                 // as Message.Ref, and the outbox's ref of a message the node sent
	ByBot     bool      `json:"by_bot,omitempty"`    // the channel says the node's bot wrote it
	BySender  bool      `json:"by_sender,omitempty"` // the channel says the person replying wrote it
	At        time.Time `json:"at,omitzero"`
	Text      string    `json:"text,omitempty"`      // cut at MaxRepliedRunes
	Forwarded bool      `json:"forwarded,omitempty"` // it is a forward: someone else's words
	HasFile   bool      `json:"has_file,omitempty"`  // a file came with it
}

// MaxRepliedRunes caps the channel's copy of a replied message.
const MaxRepliedRunes = 4096

// FileKind is what kind of file a person sent.
type FileKind string

// File kinds a channel reports.
const (
	FilePhoto     FileKind = "photo"
	FileDocument  FileKind = "document"
	FileAudio     FileKind = "audio"
	FileVoice     FileKind = "voice"
	FileVideo     FileKind = "video"
	FileVideoNote FileKind = "video_note"
	FileAnimation FileKind = "animation"
	FileSticker   FileKind = "sticker"
)

// File is a file that came with a message, as the channel describes it.
// Name and Type are the sender's text, maybe someone else's: outside
// content.
type File struct {
	Kind FileKind `json:"kind"`
	Name string   `json:"name,omitempty"` // as the sender named it
	Type string   `json:"type,omitempty"` // MIME type the channel reports
	Size int64    `json:"size,omitempty"` // bytes
}

// Decision is the payload of an event of kind "decision": a person pressed
// a button. From uses the key "from", as Message does, so the access list
// drops a stranger's press the same way it drops a stranger's message.
type Decision struct {
	Button string `json:"button"`         // the Button.ID
	From   string `json:"from"`           // principal who pressed
	Name   string `json:"name,omitempty"` // display name
	Ref    string `json:"ref,omitempty"`  // the message holding the button, for an Edit
	// Query is the channel's id of this press, for an Outgoing.Ack: the
	// handler acknowledges the press with a short notice (ruling R40).
	// "" when the channel has none.
	Query string `json:"query,omitempty"`
	// Lang is as Message.Lang, for the person who pressed.
	Lang string `json:"lang,omitempty"`
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

// Module is the payload of an event of kind "module": what happened to a
// module of the owner's, in the node's own words, for the admin it goes
// to (ruling 10 of the M4b plan). Nothing in it is the module's text: Kind
// is one of the node's kinds and Detail is the node's description, so the
// agent's note of it is not outside content (R-RUN-14).
type Module struct {
	Module    string `json:"module"`    // the full name, "local/deye-reader"
	Version   string `json:"version"`   // the manifest's version
	Kind      string `json:"kind"`      // module.exception, auth_failed, stale, module.crashloop, module.format
	Detail    string `json:"detail"`    // the node's words only
	At        int64  `json:"at_ms"`     // when it happened, Unix ms
	Principal string `json:"principal"` // the admin the turn is for: who installed the module
	Transport string `json:"transport"` // where the answer goes
}

// CommandDone is the payload of an event of kind "command.done": the call
// key of the command, whose outcome the runner stored.
type CommandDone struct {
	Key string `json:"key"`
}
