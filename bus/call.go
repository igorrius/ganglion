package bus

import (
	"context"
	"errors"
)

// HeaderDeadline is the time a caller gives up waiting for an answer, in
// Unix milliseconds: the serving side starts no run for a request past it
// (R-BUS-6).
const HeaderDeadline = "Ganglion-Deadline"

// The codes of an error answer, in the node's own words: a serving side
// never sends a module's text.
const (
	// CodeLate: the caller's deadline had passed; nothing ran.
	CodeLate = "late"
	// CodeDeep: the chain of modules is already as deep as it may be; nothing ran.
	CodeDeep = "deep"
	// CodeFormat: the request does not fit the port's in shape; nothing ran.
	CodeFormat = "format"
	// CodeFailed: the serving run failed, timed out or answered outside the port's out shape.
	CodeFailed = "failed"
	// CodeUnavailable: the serving module is not running.
	CodeUnavailable = "unavailable"
)

// ErrNoResponder is a request nobody is listening for: no service serves
// its subject.
var ErrNoResponder = errors.New("bus: no responder")

// CallError is an error answer of a served endpoint, with the code and the
// description the serving side sent.
type CallError struct {
	Code        string
	Description string
}

// Error implements error.
func (e *CallError) Error() string {
	return "bus: the call was answered with " + e.Code + ": " + e.Description
}

// Request is one request to a served endpoint: the headers (the first value
// of each) and the body.
type Request struct {
	Headers map[string]string
	Payload []byte
}

// EndpointHandler answers one request: the answer's body, or an error. A
// *CallError is sent as it is; any other error is sent as a failure with no
// text.
type EndpointHandler func(ctx context.Context, r Request) ([]byte, error)

// Endpoint is one served subject of a service: a module's port. Name is the
// port's name, Metadata describes it (the shapes), for discovery.
type Endpoint struct {
	Name     string
	Subject  string
	Metadata map[string]string
	Handler  EndpointHandler
}
