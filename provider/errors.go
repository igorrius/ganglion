package provider

import (
	"time"
)

// ErrorKind classifies a failed call for the fallback chain.
type ErrorKind string

// Error kinds.
const (
	KindRateLimit   ErrorKind = "rate_limit"  // 429
	KindTimeout     ErrorKind = "timeout"     // deadline or client timeout
	KindUnavailable ErrorKind = "unavailable" // 5xx, network
	KindAuth        ErrorKind = "auth"        // 401, 403: the key is wrong
	KindBadRequest  ErrorKind = "bad_request" // 400, 404, 422: the request itself
)

// Error is a failed provider call. Msg never holds an API key.
type Error struct {
	Provider   string
	Kind       ErrorKind
	Status     int           // HTTP status, 0 if none
	RetryAfter time.Duration // from a 429, 0 if absent
	Msg        string
}

func (e *Error) Error() string {
	return "provider " + e.Provider + ": " + string(e.Kind) + ": " + e.Msg
}
