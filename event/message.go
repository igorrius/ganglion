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
	// Group is the channel's key of the group chat the message came from,
	// the same for every topic of a forum; "" in a private chat. The
	// access filter lets a group's message through by it (R-DLV-17).
	// GroupTitle is the group's name, outside text.
	Group      string `json:"group,omitempty"`
	GroupTitle string `json:"group_title,omitempty"`
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
	// Group is as Message.Group, for the message holding the button.
	Group string `json:"group,omitempty"`
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
	Module    string `json:"module"`    // the full name, "local/demo-reader"
	Version   string `json:"version"`   // the manifest's version
	Kind      string `json:"kind"`      // module.exception, auth_failed, stale, module.crashloop, module.format
	Detail    string `json:"detail"`    // the node's words only
	At        int64  `json:"at_ms"`     // when it happened, Unix ms
	Principal string `json:"principal"` // the admin the turn is for: who installed the module
	Transport string `json:"transport"` // where the answer goes
}

// ModuleTrigger is the payload of an event of kind "module.trigger": a run
// of Producer wrote cells that Module listens to (R-RUN-15, ADR-0065). The
// module host puts one per listener and producer run, keyed by both and
// Run, so a run delivered again starts nobody twice. Nothing in it is a
// module's text: names and cell ids are the node's.
type ModuleTrigger struct {
	Module   string   `json:"module"`   // the listener, full name
	Producer string   `json:"producer"` // full name
	Run      string   `json:"run"`      // the producer's run identity
	Cells    []string `json:"cells"`    // the listened cells it wrote, sorted
	Depth    int      `json:"depth"`    // 1 for a trigger from a cron or tool run
}

// NotifyNode is what a module.notify event carries instead of a module's
// text: a closed set of the node's own lines.
type NotifyNode string

// What a module.notify event carries.
const (
	NotifyText      NotifyNode = ""      // the module's text
	NotifyLimitLine NotifyNode = "limit" // the node's line: the module reached its daily limit
	NotifyChainLine NotifyNode = "chain" // the node's line: a chain of its cells went deeper than 4 and stopped
)

// ModuleNotify is the payload of an event of kind "module.notify": a
// module's message for the admin who installed it, or a node line about
// the module (R-RUN-16, ADR-0066). Text is the module's own, cut to 1000
// characters: outside content, which goes to the owner escaped and never
// to the model; the history gets the node's note without it.
type ModuleNotify struct {
	Module  string     `json:"module"`            // full name
	Version string     `json:"version,omitempty"` // the running version's number; "" from an older node
	Text    string     `json:"text"`              // the module's text, cut; "" for a node line
	Node    NotifyNode `json:"node"`              // a node line instead of the text
	PerDay  int        `json:"per_day"`           // the manifest's daily limit
	At      int64      `json:"at_ms"`             // when the module wrote it, Unix ms
	// To is the name of the destination the message goes to, from the
	// module's notify_to; "" is the owner's private chat (R-RUN-18). The
	// event's conversation is the destination's.
	To        string `json:"to,omitempty"`
	Principal string `json:"principal"` // the admin it goes to
	Transport string `json:"transport"` // where it goes
	// Run is the id of the module's run that sent the text; "" for a node
	// line and for a message of an older node (R-RUN-20).
	Run string `json:"run,omitempty"`
}

// ModuleBuilt is the payload of an event of kind "module.built": the job
// key of a build that ended, whose answer the build queue stored, and the
// module's short name.
type ModuleBuilt struct {
	Key    string `json:"key"`    // the job's key
	Module string `json:"module"` // the module's short name
}

// SecretOutcome is how a secret request ended.
type SecretOutcome string

// Outcomes of a secret request.
const (
	SecretSet     SecretOutcome = "set"     // the value is in the store
	SecretFailed  SecretOutcome = "failed"  // the line did not open, or the store refused the value
	SecretExpired SecretOutcome = "expired" // nobody answered in time
)

// SecretClosed is the payload of an event of kind "secret.closed": a secret
// request that closed, by its name and scope, never its value (R-INT-6).
type SecretClosed struct {
	Request   string        `json:"request"` // the request's id
	Name      string        `json:"name"`    // the secret's name
	Scope     string        `json:"scope"`   // the store's scope: node, module:local/<name>, mcp:<server>
	Outcome   SecretOutcome `json:"outcome"`
	Principal string        `json:"principal"` // the admin the card went to
	Transport string        `json:"transport"` // the transport of the conversation that asked
	At        time.Time     `json:"at"`
}

// CommandDone is the payload of an event of kind "command.done": the call
// key of the command, whose outcome the runner stored.
type CommandDone struct {
	Key string `json:"key"`
}

// MemoryReview is the payload of an event of kind "memory.review": whose
// private chat to review and which session, "" for the live one, else an
// archived session's id (R-MEM-8).
type MemoryReview struct {
	Principal string `json:"principal"`
	Session   string `json:"session,omitempty"`
}

// GroupSeen is a group chat that reached the node: someone addressed the
// bot in it or added the bot to it (R-DLV-17). It is the payload of an
// event of kind "group.joined" and what the access filter reports about a
// group that is not connected. Title and Name are outside text.
type GroupSeen struct {
	Group string `json:"group"`           // the group's key, as Message.Group
	Conv  string `json:"conv"`            // where it happened: a topic's conversation, or the group's
	Title string `json:"title,omitempty"` // the group's title
	From  string `json:"from"`            // principal who addressed or added the bot
	Name  string `json:"name,omitempty"`  // their display name
}

// GroupRequest is the payload of an event of kind "group.request": the
// node asks the admin Principal, in their private chat, whether the group
// may talk to the bot.
type GroupRequest struct {
	GroupSeen
	Principal string `json:"principal"` // the admin asked
	Transport string `json:"transport"` // of the admin's private chat and of the group
}
