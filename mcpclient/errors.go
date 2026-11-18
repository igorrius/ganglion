package mcpclient

import (
	"context"
	"errors"
	"fmt"
)

// Errors a request can end with; callers decide on them with errors.Is. No
// error of this package carries a header value, an address's query or a
// response body (docs/conventions.md, section 11).
var (
	// ErrUnauthorized is a 401 answer, or no credential to send: the server
	// needs a new sign-in (R-MCP-6).
	ErrUnauthorized = errors.New("mcpclient: unauthorized")
	// ErrForbidden is a 403 answer.
	ErrForbidden = errors.New("mcpclient: forbidden")
	// ErrSessionExpired is a 404 answer to a request that carried a session
	// id. The client starts a new session once by itself.
	ErrSessionExpired = errors.New("mcpclient: session expired")
	// ErrTooLarge is an answer over the limit of its kind; nothing of it is
	// used.
	ErrTooLarge = errors.New("mcpclient: answer too large")
	// ErrNoAnswer is a request the server received and ended without
	// answering it.
	ErrNoAnswer = errors.New("mcpclient: no answer")
	// ErrNoStream is a server that has no stream for its own messages (405 to
	// the GET); the caller stops listening.
	ErrNoStream = errors.New("mcpclient: no event stream")
	// ErrProtocol is an answer that breaks the protocol.
	ErrProtocol = errors.New("mcpclient: protocol error")
	// ErrRedirect is a redirect the client refused (R-MCP-5).
	ErrRedirect = errors.New("mcpclient: redirect refused")
	// ErrUnreachable is a request that could not be made or whose answer
	// did not come in time. NotSent tells the two apart.
	ErrUnreachable = errors.New("mcpclient: unreachable")
	// ErrOutcomeUnknown is a call that is not read-only and got no answer:
	// it may have run, and the node did not repeat it (R-MCP-8).
	ErrOutcomeUnknown = errors.New("mcpclient: outcome unknown")
	// ErrBadArguments is a call whose arguments are not a JSON object.
	ErrBadArguments = errors.New("mcpclient: arguments must be a JSON object")
)

// StatusError is an HTTP status the client has no meaning for. Its text is
// the code only: a body may carry anything.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("mcpclient: the server answered status %d", e.Code)
}

// transportError is a request that did not get its answer. Its text is the
// client's own: Go's *url.Error text holds the address, which may hold a
// key.
type transportError struct {
	text    string
	timeout bool
	notSent bool
}

func (e *transportError) Error() string { return "mcpclient: " + e.text }

func (e *transportError) Is(target error) bool {
	return target == ErrUnreachable || (e.timeout && target == context.DeadlineExceeded)
}

// NotSent reports whether err is a request that provably never reached the
// server: no connection was made for it, or starting the session it needed
// failed, so it was never sent (R-MCP-8). Anything else that has no answer
// may have been processed.
func NotSent(err error) bool {
	var te *transportError
	var se *setupError
	return errors.As(err, &se) || errors.As(err, &te) && te.notSent
}

// setupError is a request that was never sent because starting the session
// it needed failed. It unwraps to that failure, so ErrUnauthorized still
// tells a caller the server needs a new sign-in.
type setupError struct{ cause error }

func (e *setupError) Error() string {
	return "mcpclient: the session could not be started; the request was not sent: " + e.cause.Error()
}
func (e *setupError) Unwrap() error { return e.cause }

// credentialError is an Auth that gave no credential. Its text never holds
// the Auth's own error; Unwrap still lets a caller test for it.
type credentialError struct {
	server string
	cause  error
}

func (e *credentialError) Error() string {
	return "mcpclient: " + e.server + ": no credential to send"
}
func (e *credentialError) Unwrap() error        { return e.cause }
func (e *credentialError) Is(target error) bool { return target == ErrUnauthorized }

// RPCError is a JSON-RPC error answer. Message is the server's text, cut to
// 512 runes: it is outside content and the caller never shows it as the
// node's own words.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcpclient: the server answered error %d", e.Code)
}
