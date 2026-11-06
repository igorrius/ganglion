// Package tool defines what the agent can do in the world: tools with a
// description in MCP's shape, the context of one call, a registry, and the
// middleware type that policies, the journal and tracing wrap calls with
// (docs/spec.md, R-POL-*; design section 4). Policies and the journal
// live in their own packages and depend on this one.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// Class is what a tool does to the world. Classes are stored in the
// journal and select policies, so their values never change.
type Class string

// Classes.
const (
	ClassRead       Class = "read"
	ClassSideEffect Class = "side_effect"
	ClassExternal   Class = "external" // returns content from outside
)

// Policy is what the core does with a call before it runs. Policies are
// stored, so their values never change.
type Policy string

// Policies.
const (
	PolicyAuto      Policy = "auto"
	PolicyConfirm   Policy = "confirm"
	PolicyForbidden Policy = "forbidden"
)

// Errors a call can end with; callers decide on them with errors.Is.
var (
	// ErrUnknown is a call to a tool that is not registered.
	ErrUnknown = errors.New("tool: unknown tool")
	// ErrForbidden is a call the policy or the caller's role does not allow.
	ErrForbidden = errors.New("tool: forbidden")
	// ErrOutOfGrant is a call outside the grant of a scheduled task.
	ErrOutOfGrant = errors.New("tool: outside the task's grant")
)

// Spec describes a tool. Name is "area.verb", e.g. "schedule.create".
type Spec struct {
	Name        string
	Description string
	InputSchema json.RawMessage // MCP inputSchema
	Class       Class
	Policy      Policy // the default for this tool; side_effect tools confirm unless stated
	AdminOnly   bool   // true for every M3b tool
	// LocalSafe says the tool sends nothing off the node, so it may run in a
	// local-only session; every other tool is refused there (ADR-0049).
	LocalSafe bool
	// AskTaintedInput makes a call with input confirm in a tainted session:
	// the tool may reach the network, where the input would leave unseen
	// (ADR-0049).
	AskTaintedInput bool
}

// TaskConvPrefix starts the conversation of a scheduled task's turns,
// "task:<job id>": its history is the task's earlier runs, and the
// policy finds the task of a call outside its grant by it (R-POL-8).
const TaskConvPrefix = "task:"

// Grant is what a scheduled task may do, approved when the task was
// created (design section 5.3).
type Grant struct {
	Tools   []string `json:"tools"`
	Hosts   []string `json:"hosts"`
	ReplyTo string   `json:"reply_to"`
}

// URLFunc reports whether an address is known: it came from the owner's
// messages or from search results of the session.
type URLFunc func(url string) bool

// Call is one tool call with the turn it belongs to.
type Call struct {
	TurnID    string // the event id
	N         int    // call number in the turn, from 1
	Conv      string
	Transport string // the transport of Conv, for messages about the call
	Principal string
	Admin     bool
	Tainted   bool   // the session's taint before this call
	LocalOnly bool   // the session holds a local-only skill's data (R-RTR-4)
	Grant     *Grant // non-nil only in a scheduled task's turn
	Depth     int    // R-AGT-4
	KnownURL  URLFunc
	// Speak writes what the person reads in their language (R-LANG-2); a
	// tool's result and errors stay in English, since a model reads them.
	Speak texts.Speaker
	Name  string
	Args  json.RawMessage
}

// Key identifies the call across reruns of its turn: the idempotency key
// of its side effects (R-DLV-6).
func (c Call) Key() string { return c.TurnID + ":" + strconv.Itoa(c.N) }

// Result is what a call returns.
type Result struct {
	Text     string
	External bool   // the text is outside content: the session becomes tainted and the text is wrapped as data
	Pending  string // non-empty: the id of a call awaiting confirmation; Text says so
	// Out are the messages the tool produces; the agent sends them with the
	// turn's replies. They are delivered even when the call returns an
	// error, so a tool must not return both unless it means to: the
	// policy's alert about a call outside a task's grant is the one such
	// case. A tool validates first and builds Out after.
	Out []event.Outgoing
}

// Func runs one call.
type Func func(ctx context.Context, c Call) (Result, error)

// Middleware wraps a Func with behaviour around every call: tracing, the
// journal, policies (spec principle 6).
type Middleware func(next Func) Func

// Chain wraps f so that mw[0] runs first.
func Chain(f Func, mw ...Middleware) Func {
	for _, m := range slices.Backward(mw) {
		f = m(f)
	}
	return f
}

// Handler is one tool.
type Handler interface {
	Spec() Spec
	Run(ctx context.Context, c Call) (Result, error)
}

// Gated is a tool that is on offer only while a condition holds, such as
// the persona tool while there is no persona. The registry leaves a tool
// that is not on offer out of its listing; the tool still refuses its own
// calls then, because a name the model made up for a tool it was not
// offered, and a call parked earlier, reach it all the same. Offered
// fails closed: when it cannot tell, the tool is not offered.
type Gated interface {
	Offered() bool
}

// MaxQuestionRunes is the longest confirmation question the policy shows;
// it cuts a longer one. A Questioner that must show its whole action
// refuses a call whose question would be longer (ruling R34).
const MaxQuestionRunes = 3000

// Questioner is a tool that words the owner's confirmation question
// itself, from the call's arguments, so the owner sees what they approve
// in plain words (a scheduled task's grant, design section 5.3). The
// policy still removes secrets from it. An error means the call is
// invalid: it is refused at once, without a question.
type Questioner interface {
	Question(ctx context.Context, c Call) (string, error)
}

// closingTag matches a closing external tag in any case and spacing.
var closingTag = regexp.MustCompile(`(?i)</(\s*external)`)

var sourceEscaper = strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;", "\n", " ")

// wrapperOpen is the opening tag of source's wrapper.
func wrapperOpen(source string) string {
	return `<external source="` + sourceEscaper.Replace(source) + "\">\n"
}

const wrapperClose = "\n</external>"

// ExternalClose is the end of every wrapper WrapExternal makes, so a
// renderer that shortens a wrapped text can keep the wrapper closed.
const ExternalClose = wrapperClose

// WrapExternal marks text from outside as data for the model. A closing
// tag inside text is broken, so the text cannot end the wrapper early.
func WrapExternal(source, text string) string {
	return wrapperOpen(source) + closingTag.ReplaceAllString(text, `<\/$1`) + wrapperClose
}

// StripExternal returns s without the wrappers WrapExternal made and the
// text inside them: what is left is the text around them. A wrapper that
// is not closed takes the rest of s with it.
func StripExternal(s string) string {
	const open = `<external source="`
	var b strings.Builder
	for {
		before, rest, found := strings.Cut(s, open)
		b.WriteString(before)
		if !found {
			return b.String()
		}
		_, s, found = strings.Cut(rest, wrapperClose)
		if !found {
			return b.String()
		}
	}
}

// Unwrap returns the text inside the wrapper WrapExternal made for
// source; ok is false when s is not such a wrapper. Broken closing tags
// stay broken.
func Unwrap(source, s string) (text string, ok bool) {
	inner, ok := strings.CutPrefix(s, wrapperOpen(source))
	if !ok {
		return "", false
	}
	return strings.CutSuffix(inner, wrapperClose)
}
