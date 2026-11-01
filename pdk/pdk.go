// Package pdk is the module-side kit for ganglion WASM modules: the Host a
// module talks to and the wire types shared with the runner.
//
// Module logic depends only on the Host interface, so it can be tested with
// plain `go test` and then compiled to WASM unchanged. The WASM glue that
// implements Host on top of the runner's host functions lives in
// wasm_wasip1.go and is built only for GOOS=wasip1.
//
// This is the P0 "own ABI" variant; ADR-0011 decides between it and Extism.
package pdk

import (
	"encoding/json"
	"errors"
	"time"
)

// Host is everything a module may ask of the runner. Each call is checked
// against the module's manifest: an ungranted domain or secret fails.
type Host interface {
	// Fetch performs an HTTP request to a domain allowed by the manifest.
	Fetch(req Request) (Response, error)
	// StateGet reads a value from the module's persistent state.
	StateGet(key string) (string, bool)
	// StatePut writes a value to the module's persistent state.
	StatePut(key, value string)
	// Secret returns a secret granted by the manifest; ok is false if it is
	// not granted or not set.
	Secret(name string) (value string, ok bool)
	// Config returns a value from the manifest's config block.
	Config(key string) (value string, ok bool)
	// Now returns the current time.
	Now() time.Time
	// Log writes a line to the runner's log.
	Log(msg string)
}

// Request is an HTTP request made through the host.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// Response is the host's answer to a Request. Error is set when the request
// could not be made at all (denied by policy, network failure, timeout).
type Response struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Input is what the runner passes to a module's handle export.
type Input struct {
	Trigger string          `json:"trigger"`           // "cron", "tool:<name>", "event:<kind>"
	Payload json.RawMessage `json:"payload,omitempty"` // trigger-specific data
}

// Error kinds a module reports. The runner maps them to escalation events
// (spec 7.5, the manifest's escalate block).
const (
	KindException  = "module.exception" // unexpected data or a failed read
	KindAuthFailed = "auth_failed"      // credentials rejected; do not retry
)

// Output is what a module's handle export returns.
type Output struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"` // module-specific JSON
	Kind   string          `json:"kind,omitempty"`   // set when OK is false
	Error  string          `json:"error,omitempty"`
}

// kindError tags an error with an Output kind.
type kindError struct {
	kind string
	err  error
}

func (e kindError) Error() string { return e.err.Error() }
func (e kindError) Unwrap() error { return e.err }

// WithKind tags err so the module reports it as kind (for example
// KindAuthFailed) instead of the default KindException.
func WithKind(kind string, err error) error {
	return kindError{kind: kind, err: err}
}

// Encode turns a handler's result into the Output a module returns.
func Encode(result any, err error) Output {
	if err != nil {
		kind := KindException
		if ke, ok := errors.AsType[kindError](err); ok {
			kind = ke.kind
		}
		return Output{Kind: kind, Error: err.Error()}
	}
	b, merr := json.Marshal(result)
	if merr != nil {
		return Output{Kind: KindException, Error: "encode result: " + merr.Error()}
	}
	return Output{OK: true, Result: b}
}
