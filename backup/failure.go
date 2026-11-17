package backup

import (
	"context"
	"errors"
	"net"
	"strconv"
)

// Failure is why a backup failed, a closed set the alert and /status turn
// into a fixed text. It never carries an error's text, which may hold a path
// or an address.
type Failure string

// The failures.
const (
	// FailureNone is no failure: the backup succeeded.
	FailureNone Failure = ""
	// FailureRefused is a response of the bucket's endpoint other than 2xx.
	FailureRefused Failure = "refused"
	// FailureNetwork is a bucket that could not be reached in time.
	FailureNetwork Failure = "network"
	// FailureChanged is a file that changed while it was archived.
	FailureChanged Failure = "changed"
	// FailureIntegrity is a database copy that failed its integrity check.
	FailureIntegrity Failure = "integrity"
	// FailureOther is any other failure; the node's log says which.
	FailureOther Failure = "other"
)

// classify is the Failure of err and its Detail.
func classify(err error) (Failure, string) {
	if se, ok := errors.AsType[*StatusError](err); ok {
		detail := strconv.Itoa(se.Status)
		if se.Code != "" {
			detail += " " + se.Code
		}
		return FailureRefused, detail
	}
	var ne net.Error
	switch {
	case errors.Is(err, ErrChanged):
		return FailureChanged, ""
	case errors.Is(err, ErrIntegrity):
		return FailureIntegrity, ""
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne):
		return FailureNetwork, ""
	}
	return FailureOther, ""
}
